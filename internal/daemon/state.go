package daemon

import (
	"fmt"
	"time"

	"github.com/multiplexer-si/openplexer/internal/policy"
)

// State is the state of the stored policy.
//
//	Absent    load of a token that passes admission      -> Verified
//	Verified  the clock passes StaleAt                   -> Stale
//	Verified  load of the same bundle or a newer version -> Verified
//	Stale     load of the same bundle or a newer version -> Verified
//	Verified  a token, key set or config change causes
//	Stale     a reopen check to fail                     -> Rejected
//	Rejected  load of a token that passes admission      -> Verified
//	Rejected  the token passes reopen checks again       -> Verified or Stale
//	any       the stored policy is removed               -> Absent
//
// A load that fails changes no state. The policy in force stays.
type State int

const (
	// Absent means that no policy is stored.
	Absent State = iota
	// Verified means that the stored token verifies and is not stale.
	Verified
	// Stale means that the stored token verifies, and that the source has
	// not confirmed it for the configured time.
	Stale
	// Rejected means that a policy is stored and cannot be used.
	Rejected
)

func (s State) String() string {
	return [...]string{"absent", "verified", "stale", "rejected"}[s]
}

// Status describes the stored policy for a person and for an audit record.
// ProjectID, Version, ConfirmedAt, StaleAt, and Role are set only when the
// state is Verified or Stale.
type Status struct {
	State          State
	Detail         string
	ProjectID      string
	Version        int64
	ConfirmedAt    time.Time
	StaleAt        time.Time
	HubConfirmedAt time.Time
	MaxPolicyAge   time.Duration
	// Member is the configured member id of the developer, or "".
	Member string
	// Role is the role that the bundle gives to Member, or "".
	Role string
}

// Enforced reports whether a verified policy decides actions.
func (s Status) Enforced() bool {
	return s.State == Verified || s.State == Stale
}

type storedPolicy struct {
	Token          string    `json:"token"`
	ConfirmedAt    time.Time `json:"confirmed_at"`
	HubConfirmedAt time.Time `json:"hub_confirmed_at,omitzero"`
	HubBinding     string    `json:"hub_binding,omitempty"`
}

func classify(home string, cfg config, now time.Time, verifier policy.BundleVerifier) (Status, *policy.Verified) {
	rejected := func(format string, args ...any) (Status, *policy.Verified) {
		return Status{State: Rejected, Detail: fmt.Sprintf(format, args...)}, nil
	}
	stored, err := readStoredPolicy(home)
	switch {
	case err != nil:
		return rejected("the stored policy cannot be read: %v", err)
	case stored == nil:
		return Status{State: Absent, Detail: "no policy is loaded"}, nil
	}
	signed, err := policy.ReadSignedBundle(stored.Token)
	if err != nil {
		return rejected("the stored policy does not verify against the key set: %v", err)
	}
	keys, pin, err := readPinnedKeys(home)
	switch {
	case err != nil:
		return rejected("the key set cannot be read: %v", err)
	case keys == nil:
		return rejected("no key set is configured, so the stored policy cannot be verified")
	}
	verified, err := verifier.Verify(signed, *keys)
	if err != nil {
		return rejected("the stored policy does not verify against the key set: %v", err)
	}
	status := Status{
		State:       Verified,
		ProjectID:   verified.Bundle.ProjectID,
		Version:     verified.Bundle.Version,
		ConfirmedAt: stored.ConfirmedAt,
		StaleAt:     stored.ConfirmedAt.Add(cfg.staleAfter()),
	}
	status.Detail = fmt.Sprintf("version %d of project %s, confirmed %s", status.Version, status.ProjectID, stored.ConfirmedAt.UTC().Format(time.RFC3339))
	if cfg.hasHubSettings() {
		if client, err := cfg.client(); err == nil && stored.HubBinding == client.binding(pin, cfg.HubTrustRevision) {
			status.HubConfirmedAt = stored.HubConfirmedAt
		}
		status.ConfirmedAt = status.HubConfirmedAt
		status.StaleAt = time.Time{}
		status.Detail = fmt.Sprintf("version %d of project %s", status.Version, status.ProjectID)
		if !status.HubConfirmedAt.IsZero() {
			status.StaleAt = status.HubConfirmedAt.Add(cfg.maxPolicyAge())
		}
	} else if !now.Before(status.StaleAt) {
		status.State = Stale
		status.Detail += fmt.Sprintf(", stale since %s", status.StaleAt.UTC().Format(time.RFC3339))
	}
	return status, verified
}
