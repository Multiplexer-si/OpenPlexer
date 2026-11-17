package daemon

import (
	"fmt"
	"time"

	"github.com/multiplexer-si/openplexer/internal/policy"
)

// State is the state of the stored policy. The home directory records two
// facts, the signed token and the time that the source last confirmed it.
// classify computes the state from them, the key set, and the clock, so no
// state is stored and no two flags can disagree.
//
//	Absent    load of a token that verifies              -> Verified
//	Verified  the clock passes StaleAt                   -> Stale
//	Verified  load of the same bundle or a newer version -> Verified
//	Stale     load of the same bundle or a newer version -> Verified
//	Verified  the stored token or the key set changes,
//	Stale     and the token no longer verifies           -> Rejected
//	Rejected  load of a token that verifies              -> Verified
//	Rejected  the key set verifies the token again       -> Verified or Stale
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
	// not confirmed it for the configured time. The gate still enforces it.
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
	State       State
	Detail      string
	ProjectID   string
	Version     int64
	ConfirmedAt time.Time
	StaleAt     time.Time
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
	Token       string    `json:"token"`
	ConfirmedAt time.Time `json:"confirmed_at"`
}

// classify computes the state of the stored policy. stored and keys are the
// two files of the home directory, and nil for a file that does not exist.
func classify(stored *storedPolicy, storeProblem error, keys *policy.KeySet, keysProblem error, staleAfter time.Duration, now time.Time) (Status, *policy.Verified) {
	if stored == nil && storeProblem == nil {
		return Status{State: Absent, Detail: "no policy is loaded"}, nil
	}
	rejected := func(format string, args ...any) (Status, *policy.Verified) {
		return Status{State: Rejected, Detail: fmt.Sprintf(format, args...)}, nil
	}
	if storeProblem != nil {
		return rejected("the stored policy cannot be read: %v", storeProblem)
	}
	if keysProblem != nil {
		return rejected("the key set cannot be read: %v", keysProblem)
	}
	if keys == nil {
		return rejected("no key set is configured, so the stored policy cannot be verified")
	}
	verified, err := policy.VerifySignedBundle(stored.Token, *keys)
	if err != nil {
		return rejected("the stored policy does not verify against the key set: %v", err)
	}
	status := Status{
		State:       Verified,
		ProjectID:   verified.Bundle.ProjectID,
		Version:     verified.Bundle.Version,
		ConfirmedAt: stored.ConfirmedAt,
		StaleAt:     stored.ConfirmedAt.Add(staleAfter),
	}
	status.Detail = fmt.Sprintf("version %d of project %s, confirmed %s", status.Version, status.ProjectID, stored.ConfirmedAt.UTC().Format(time.RFC3339))
	if !now.Before(status.StaleAt) {
		status.State = Stale
		status.Detail += fmt.Sprintf(", stale since %s", status.StaleAt.UTC().Format(time.RFC3339))
	}
	return status, verified
}
