package hub

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/multiplexer-si/openplexer/internal/room"
)

const (
	defaultTTL  = 3600
	implicitTTL = 600
	maxTTL      = int64((1<<63 - 1) / time.Second)
)

type lease struct {
	ID        string     `json:"id"`
	Room      string     `json:"room"`
	Member    string     `json:"on_behalf_of"`
	Agent     agentInput `json:"agent"`
	Paths     []string   `json:"paths"`
	Intent    string     `json:"intent"`
	Mode      string     `json:"mode"`
	ExpiresAt time.Time  `json:"expires_at"`
}

func (s *Store) claimEndpoint(run func(*state, request, time.Time) (any, error)) http.HandlerFunc {
	return s.roomEndpoint(maxRoomBody, func(st *state, req request) (any, error) {
		now := s.now().UTC().Truncate(time.Millisecond)
		swept, err := st.expireClaims(now)
		if err != nil {
			return nil, err
		}
		result, operationErr := run(st, req, now)
		// A failed renew or release may be the operation that found an expiry.
		if swept || operationErr == nil {
			if err := s.save(st); err != nil {
				return nil, err
			}
		}
		return result, operationErr
	})
}

func (st *state) expireClaims(now time.Time) (bool, error) {
	swept := false
	for _, id := range slices.Sorted(maps.Keys(st.Claims)) {
		c := st.Claims[id]
		if !c.ExpiresAt.After(now) {
			if err := st.endClaim(c, "expired", now); err != nil {
				return false, err
			}
			swept = true
		}
	}
	return swept, nil
}

func createClaim(st *state, req request, now time.Time) (any, error) {
	var input struct {
		Agent      agentInput `json:"agent"`
		Item       *string    `json:"item"`
		Paths      []string   `json:"paths"`
		Intent     *string    `json:"intent"`
		Mode       string     `json:"mode"`
		Implicit   bool       `json:"implicit"`
		TTLSeconds *int64     `json:"ttl_seconds"`
	}
	if err := json.Unmarshal(req.body, &input); err != nil || input.Item == nil || input.Paths == nil || input.Intent == nil {
		return nil, fmt.Errorf("%w: claim requires item, paths and intent", errInvalid)
	}
	agent, err := input.Agent.session(req.member)
	if err != nil {
		return nil, err
	}
	mode := input.Mode
	if mode == "" {
		mode = "shared"
	}
	if mode != "shared" && mode != "exclusive" {
		return nil, fmt.Errorf("%w: claim mode must be shared or exclusive", errInvalid)
	}
	ttl := int64(defaultTTL)
	if input.Implicit {
		ttl = implicitTTL
	}
	if input.TTLSeconds != nil {
		ttl = *input.TTLSeconds
	}
	expires, err := leaseExpiry(now, ttl)
	if err != nil {
		return nil, err
	}
	c := lease{
		ID: rand.Text(), Room: req.roomID(), Member: req.member, Agent: agent,
		Paths: input.Paths, Intent: *input.Intent, Mode: mode, ExpiresAt: expires,
	}
	conflicts := []lease{}
	repo := st.Projects[c.Room].Repo
	for _, id := range slices.Sorted(maps.Keys(st.Claims)) {
		other := st.Claims[id]
		if st.Projects[other.Room].Repo == repo && other.Agent.ID != c.Agent.ID &&
			(c.Mode == "exclusive" || other.Mode == "exclusive") && room.PathsOverlap(other.Paths, c.Paths) {
			conflicts = append(conflicts, other)
		}
	}
	event, err := st.appendEvent(c.Room, c.Member, c.Agent, "claim", map[string]any{
		"claim_id": c.ID, "item": *input.Item, "paths": c.Paths, "intent": c.Intent,
		"ttl_seconds": ttl, "mode": mode, "implicit": input.Implicit,
	}, now, "")
	if err != nil {
		return nil, err
	}
	st.Claims[c.ID] = c
	return map[string]any{"granted": c, "conflicts": conflicts, "event": room.EventMessage(event)}, nil
}

func renewClaim(st *state, req request, now time.Time) (any, error) {
	var input struct {
		SessionID  string `json:"session_id"`
		TTLSeconds int64  `json:"ttl_seconds"`
	}
	if err := json.Unmarshal(req.body, &input); err != nil {
		return nil, fmt.Errorf("%w: expected session_id and ttl_seconds", errInvalid)
	}
	c, err := st.ownClaim(req, input.SessionID)
	if err != nil {
		return nil, err
	}
	c.ExpiresAt, err = leaseExpiry(now, input.TTLSeconds)
	if err != nil {
		return nil, err
	}
	// E8 leaves the original Room event and the receiver's expiry intact.
	st.Claims[c.ID] = c
	return c, nil
}

func releaseClaim(st *state, req request, now time.Time) (any, error) {
	var input struct {
		SessionID string `json:"session_id"`
		Outcome   string `json:"outcome"`
	}
	if err := json.Unmarshal(req.body, &input); err != nil {
		return nil, fmt.Errorf("%w: expected session_id and optional outcome", errInvalid)
	}
	if input.Outcome == "expired" {
		return nil, fmt.Errorf("%w: expired is reserved for the hub", errInvalid)
	}
	c, err := st.ownClaim(req, input.SessionID)
	if err != nil {
		return nil, err
	}
	if err := st.endClaim(c, input.Outcome, now); err != nil {
		return nil, err
	}
	return c, nil
}

func (st *state) ownClaim(req request, sessionID string) (lease, error) {
	if !idPattern.MatchString(sessionID) {
		return lease{}, fmt.Errorf("%w: valid session_id required", errInvalid)
	}
	c, ok := st.Claims[req.PathValue("claim")]
	if !ok || c.Room != req.roomID() || c.Agent.ID != req.member+"/"+sessionID {
		return lease{}, errNotFound
	}
	return c, nil
}

func (st *state) endClaim(c lease, outcome string, now time.Time) error {
	body := map[string]string{"claim_id": c.ID}
	if outcome != "" {
		body["outcome"] = outcome
	}
	if _, err := st.appendEvent(c.Room, c.Member, c.Agent, "release", body, now, ""); err != nil {
		return err
	}
	delete(st.Claims, c.ID)
	return nil
}

func leaseExpiry(now time.Time, ttl int64) (time.Time, error) {
	if ttl < 1 || ttl > maxTTL {
		return time.Time{}, fmt.Errorf("%w: ttl_seconds must be between 1 and %d", errInvalid, maxTTL)
	}
	return now.Add(time.Duration(ttl) * time.Second), nil
}
