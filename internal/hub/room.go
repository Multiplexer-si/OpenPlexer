package hub

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/multiplexer-si/openplexer/internal/audit"
	"github.com/multiplexer-si/openplexer/internal/room"
)

const (
	maxRoomBody  = 64 << 10
	replayPage   = 100
	timestampFmt = "2006-01-02T15:04:05.000Z"
	humanVendor  = "human"
)

type eventRecord struct {
	Event  json.RawMessage `json:"event"`
	Key    string          `json:"key,omitempty"`
	Member string          `json:"member,omitempty"`
}

type agentInput struct {
	ID     string `json:"id"`
	Vendor string `json:"vendor"`
	Parent string `json:"parent,omitempty"`
}

func (a agentInput) session(member string) (agentInput, error) {
	if !idPattern.MatchString(a.ID) || !idPattern.MatchString(a.Vendor) || a.Vendor == humanVendor ||
		(a.Parent != "" && (!idPattern.MatchString(a.Parent) || a.Parent == a.ID)) {
		return agentInput{}, fmt.Errorf("%w: valid agent id, vendor and optional parent required", errInvalid)
	}
	a.ID = member + "/" + a.ID
	if a.Parent != "" {
		a.Parent = member + "/" + a.Parent
	}
	return a, nil
}

func (s *Store) roomRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/rooms/{room}/events", s.roomEndpoint(maxRoomBody, s.postEvent))
	mux.HandleFunc("GET /api/v1/rooms/{room}/events", s.roomEndpoint(0, replay))
	mux.HandleFunc("GET /api/v1/rooms/{room}/stream", s.streamRoom)
	mux.HandleFunc("POST /api/v1/rooms/{room}/stream", s.streamRoom)
	mux.HandleFunc("POST /api/v1/rooms/{room}/claims", s.claimEndpoint(createClaim))
	mux.HandleFunc("POST /api/v1/rooms/{room}/claims/{claim}/renew", s.claimEndpoint(renewClaim))
	mux.HandleFunc("POST /api/v1/rooms/{room}/claims/{claim}/release", s.claimEndpoint(releaseClaim))
}

func (r request) roomID() string { return r.PathValue("room") }

func (s *Store) roomEndpoint(limit int64, run func(*state, request) (any, error)) http.HandlerFunc {
	return s.endpoint(limit, func(st *state, req request) (any, error) {
		// Each project has a Room with the same id.
		if _, ok := st.Projects[req.roomID()]; !ok {
			return nil, errNotFound
		}
		return run(st, req)
	})
}

func (s *Store) postEvent(st *state, req request) (any, error) {
	post, err := room.ParseEventPost(req.body)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errInvalid, err)
	}
	var agent agentInput
	switch post.Type {
	case "claim", "release", "decision.confirm", "policy.override", "policy.override.revoked":
		return nil, errForbidden
	case "human":
		if post.Agent != nil {
			return nil, errForbidden
		}
		agent = agentInput{ID: req.member, Vendor: humanVendor}
	default:
		if post.Agent == nil {
			return nil, fmt.Errorf("%w: agent is required", errInvalid)
		}
		agent, err = agentInput(*post.Agent).session(req.member)
		if err != nil {
			return nil, err
		}
	}
	key := req.Header.Get("Idempotency-Key")
	if len(key) > 128 {
		return nil, fmt.Errorf("%w: idempotency key exceeds 128 bytes", errInvalid)
	}
	if key != "" {
		for _, record := range st.Rooms[req.roomID()] {
			if record.Key == key && record.Member == req.member {
				event, err := room.ParseEvent(record.Event)
				if err != nil {
					return nil, err
				}
				return room.EventMessage(event), nil
			}
		}
	}
	delete(post.Body, "flagged")
	now := s.now()
	event, err := st.appendEvent(req.roomID(), req.member, agent, post.Type, post.Body, now, key)
	if err != nil {
		return nil, fmt.Errorf("%w: event: %w", errInvalid, err)
	}
	if event.Type() == "policy.denied" {
		// Link to the event without copying arbitrary Room text into the audit log.
		if err := st.appendAudit(audit.Record{
			ActorMemberID: &req.member, AgentSessionID: &agent.ID,
			Action: "event.policy-denied", Target: "event:" + event.ID(),
			Meta: map[string]any{"room_id": event.Mission(), "seq": event.Seq(), "event_id": event.ID()},
		}, now); err != nil {
			return nil, err
		}
	}
	return room.EventMessage(event), s.save(st)
}

func (st *state) appendEvent(roomID, member string, agent agentInput, kind string, body any, now time.Time, key string) (room.Event, error) {
	events := st.Rooms[roomID]
	text, err := json.Marshal(map[string]any{
		"id": rand.Text(), "seq": len(events) + 1, "mission": roomID,
		"type": kind, "agent": agent, "on_behalf_of": member,
		"at": now.UTC().Format(timestampFmt), "body": body,
	})
	if err != nil {
		return room.Event{}, err
	}
	event, err := room.ParseEvent(text)
	if err != nil {
		return room.Event{}, err
	}
	record := eventRecord{Event: text, Key: key}
	if key != "" {
		record.Member = member
	}
	st.Rooms[roomID] = append(events, record)
	return event, nil
}

func replay(st *state, req request) (any, error) {
	after, err := replayCursor(req.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	return roomPage(st.Rooms[req.roomID()], after)
}

func replayCursor(rawQuery string) (uint64, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid query string", errInvalid)
	}
	after := uint64(0)
	if values, present := query["last_seq"]; present {
		if len(values) != 1 {
			return 0, fmt.Errorf("%w: last_seq must be supplied once", errInvalid)
		}
		after, err = strconv.ParseUint(values[0], 10, 53)
		if err != nil {
			return 0, fmt.Errorf("%w: last_seq must be a nonnegative safe integer", errInvalid)
		}
	}
	return after, nil
}

func roomPage(events []eventRecord, after uint64) ([]room.Message, error) {
	messages := []room.Message{}
	if after >= uint64(len(events)) {
		return messages, nil
	}
	start := int(after)
	for _, record := range events[start:min(start+replayPage, len(events))] {
		event, err := room.ParseEvent(record.Event)
		if err != nil {
			return nil, err
		}
		messages = append(messages, room.EventMessage(event))
	}
	return messages, nil
}
