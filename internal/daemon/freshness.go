package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/multiplexer-si/openplexer/internal/policy"
)

const (
	RefreshBudget     = 2 * time.Second
	automaticBackoff  = 5 * time.Second
	automaticLockFile = "policy-refresh.lock"
	automaticFailFile = "policy-refresh-failure.json"
	StalePolicyReason = "the hub policy is stale. Run `openplexer policy sync` for this home"
)

func validMaxPolicyAge(seconds int) bool { return seconds >= 60 && seconds <= 3600 }

func (c config) maxPolicyAge() time.Duration {
	if !c.hasHubSettings() {
		return 0
	}
	if c.MaxPolicyAgeSeconds == 0 {
		return 5 * time.Minute
	}
	return time.Duration(c.MaxPolicyAgeSeconds) * time.Second
}

func hubPolicyStale(confirmed time.Time, limit time.Duration, now time.Time) bool {
	return confirmed.IsZero() || now.Before(confirmed) || now.Sub(confirmed) >= limit
}

func (s Status) hubStaleAt(now time.Time) bool {
	return s.MaxPolicyAge != 0 && s.Enforced() &&
		hubPolicyStale(s.HubConfirmedAt, s.MaxPolicyAge, now)
}

func (s Status) hubNeedsRefresh() bool {
	return s.MaxPolicyAge != 0 && (s.State == Rejected || s.State == Stale)
}

// HookDecision is the policy, configuration and refresh result for one home
// in one hook call. The decision uses the opened state and its captured time.
type HookDecision struct {
	gate            Gate
	status          Status
	at              time.Time
	refreshRequired bool
	refreshErr      error
}

// OpenForHook prepares one home. The caller supplies a refresh deadline shared
// across all its homes.
func OpenForHook(ctx context.Context, home string, now func() time.Time) (HookDecision, error) {
	g, err := openPolicy(home, now, policy.BundleVerifier{})
	if err != nil {
		return HookDecision{}, err
	}
	d := g.prepareHook(ctx)
	return d, d.refreshErr
}

func (g *Gate) prepareHook(ctx context.Context) HookDecision {
	at, started := g.now(), time.Now()
	d := HookDecision{gate: *g, at: at, status: g.statusAt(at).localStatusAt(at)}
	// Anchor fetch, reopen and audit timestamps to one wall-clock reading.
	// Monotonic elapsed time lets a newly confirmed policy be used after
	// waiting for a peer or doing our own fetch.
	d.gate.now = func() time.Time { return at.Add(time.Since(started)) }
	d.refreshRequired = d.status.hubNeedsRefresh()
	if d.refreshRequired {
		d.refreshErr = d.refresh(ctx)
		d.at = d.gate.now()
	}
	return d
}

func (d HookDecision) Status() Status { return d.status }

func (d HookDecision) ExecGated() bool { return !d.status.Enforced() || d.gate.ExecGated() }

// RefreshBlocked applies even when the call produces no policy queries.
func (d HookDecision) RefreshBlocked() bool {
	return d.refreshRequired && (d.refreshErr != nil || !d.status.freshHubPolicy())
}

func (d HookDecision) DecideContext(ctx context.Context, agent string, first policy.Query, rest ...policy.Query) (Verdict, error) {
	if d.refreshErr != nil {
		return Verdict{Policy: d.status, Query: first}, d.refreshErr
	}
	return d.gate.decideAt(ctx, agent, d.status, d.at, first, rest...)
}

func (d HookDecision) QueueDenial(v Verdict, agent string) error {
	return d.gate.QueueDenial(v, agent)
}

func (s Status) freshHubPolicy() bool { return s.MaxPolicyAge != 0 && s.State == Verified }

func (d *HookDecision) refresh(ctx context.Context) error {
	g := &d.gate
	unlock, err := lockHomeFile(ctx, g.home, automaticLockFile)
	if err != nil {
		if ctx.Err() != nil {
			return nil // The stale or rejected snapshot still blocks the call.
		}
		return err
	}
	defer unlock()
	if err := g.reopenHub(); err != nil {
		return err
	}
	// An unchanged confirmation cannot become fresh while this call waits.
	at := d.at
	if !g.status.HubConfirmedAt.Equal(d.status.HubConfirmedAt) {
		at = g.now()
	}
	d.status = g.statusAt(at)
	if d.status.freshHubPolicy() {
		return nil
	}
	var failedAt time.Time
	if _, err := readJSON(g.home, automaticFailFile, map[string]any{"failed_at": &failedAt}); err != nil {
		return err
	}
	now := g.now().UTC()
	if ctx.Err() != nil || !failedAt.IsZero() && !now.Before(failedAt) && now.Sub(failedAt) < automaticBackoff {
		return nil
	}
	syncErr := g.sync(ctx, true)
	reopenErr := g.reopenHub()
	if reopenErr == nil {
		if syncErr == nil || !g.status.HubConfirmedAt.Equal(d.status.HubConfirmedAt) {
			at = g.now()
		}
		d.status = g.statusAt(at)
	}
	if syncErr != nil || reopenErr != nil || !d.status.freshHubPolicy() {
		_ = writeJSON(g.home, automaticFailFile, map[string]time.Time{"failed_at": g.now().UTC()})
	}
	if reopenErr != nil {
		return reopenErr
	}
	if d.status.freshHubPolicy() {
		// A sync can fail after publishing a fresh policy. Its error still
		// blocks this call. Stale and rejected states have their own denials.
		return syncErr
	}
	return nil
}

func (g *Gate) reopenHub() error {
	cfg, err := readConfig(g.home)
	if err != nil {
		return err
	}
	g.config = cfg
	_, readErr := g.readPolicy()
	return g.requireHubPolicy(readErr)
}

func (g *Gate) requireHubPolicy(readErr error) error {
	if !g.config.hasHubSettings() {
		return errors.New("config.json no longer contains hub settings")
	}
	if readErr != nil {
		return readErr
	}
	if g.status.State == Absent {
		return errors.New("policy.json is missing")
	}
	return nil
}
