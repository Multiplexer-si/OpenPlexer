package daemon

import (
	"context"
	"time"
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

// HubStale distinguishes an expired cached hub policy from a local advisory
// and from a policy that is absent or cannot be verified.
func (g *Gate) HubStale() bool {
	return g.status.hubStaleAt(g.now())
}

// RefreshIfStale reopens the policy under the automatic refresh lock and
// after any attempt. A failed request leaves the stale gate available to deny
// and audit. The caller supplies a deadline shared across its homes.
func (g *Gate) RefreshIfStale(ctx context.Context) error {
	if !g.HubStale() {
		return nil
	}
	unlock, err := lockHomeFile(ctx, g.home, automaticLockFile)
	if err != nil {
		return g.reopen()
	}
	defer unlock()
	if err := g.reopen(); err != nil || !g.HubStale() {
		return err
	}
	var failedAt time.Time
	_, _ = readJSON(g.home, automaticFailFile, map[string]any{"failed_at": &failedAt})
	now := g.now().UTC()
	if ctx.Err() != nil || !failedAt.IsZero() && !now.Before(failedAt) && now.Sub(failedAt) < automaticBackoff {
		return nil
	}
	_, syncErr := g.Sync(ctx)
	if err := g.reopen(); err != nil {
		return err
	}
	if syncErr != nil && g.HubStale() {
		_ = writeJSON(g.home, automaticFailFile, map[string]time.Time{"failed_at": g.now().UTC()})
	}
	return nil
}

func (g *Gate) reopen() error {
	cfg, err := readConfig(g.home)
	if err != nil {
		return err
	}
	g.config = cfg
	g.read()
	return nil
}
