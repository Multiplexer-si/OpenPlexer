package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/multiplexer-si/openplexer/internal/audit"
	"github.com/multiplexer-si/openplexer/internal/policy"
)

// Gate decides the actions of a coding agent under the policy that is
// stored in one home directory. It is the view of one short process: Open
// reads the home once, and only Load changes what the gate holds.
type Gate struct {
	home     string
	now      func() time.Time
	config   config
	status   Status
	bundle   *policy.Bundle
	verifier policy.BundleVerifier
}

// Open reads the home directory and verifies the stored policy against the
// configured key set. It writes nothing. A problem with the stored policy
// or with the key set is a State and not an error, so that a caller can
// still ask for a verdict. The error is for a configuration that cannot be
// read.
func Open(home string, now func() time.Time) (*Gate, error) {
	return openGate(home, now, policy.BundleVerifier{})
}

func openGate(home string, now func() time.Time, verifier policy.BundleVerifier) (*Gate, error) {
	config, err := readConfig(home)
	if err != nil {
		return nil, err
	}
	g := &Gate{home: home, now: now, config: config, verifier: verifier}
	g.read()
	return g, nil
}

func (g *Gate) read() *policy.Verified {
	status, verified := classify(g.home, g.config.staleAfter(), g.now(), g.verifier)
	status.Member = g.config.MemberID
	g.bundle = nil
	if verified != nil {
		if err := g.config.checkBundle(verified.Bundle); err != nil {
			status = Status{State: Rejected, Detail: err.Error(), Member: g.config.MemberID}
			verified = nil
		}
	}
	if verified != nil {
		g.bundle = verified.Bundle
		status.Role = verified.Bundle.Members[status.Member]
	}
	g.status = status
	return verified
}

// Status returns the state of the stored policy as this gate read it.
func (g *Gate) Status() Status {
	return g.status
}

// ExecGated reports whether the policy in force gives the role of the
// configured member an exec grant. A caller asks it to choose whether a
// shell command is also judged as an exec action. It reports false when no
// verified policy is in force.
func (g *Gate) ExecGated() bool {
	return g.bundle != nil && len(g.bundle.Roles[g.status.Role].Exec) > 0
}

// ErrRejected is the error of a Load whose bundle the gate refuses.
var ErrRejected = errors.New("the bundle is rejected")

func bundleSizeError(err error) bool {
	return errors.Is(err, policy.ErrCompactBundleTooLarge) || errors.Is(err, policy.ErrBundlePayloadTooLarge)
}

// Load fetches a signed bundle from the source and makes it the policy in
// force. The bundle must verify against the configured key set and meet its
// project's version floor. While a policy is in force, the bundle must also
// be for the same project, and it must be a newer version or the same bundle
// again. The same bundle again confirms the policy, which makes it fresh.
//
// Load returns the status after the load. With an error, the policy in
// force stays as it was: a source that is not available and a bundle that
// is rejected both change nothing. The error of a rejected bundle wraps
// ErrRejected. An error that wraps only ErrNotRecorded means that the
// bundle is in force.
func (g *Gate) Load(ctx context.Context, source Source) (Status, error) {
	token, err := source.Fetch(ctx)
	if err != nil && !bundleSizeError(err) {
		return g.status, fmt.Errorf("the policy source is not available, and the policy in force stays: %w", err)
	}
	var signed *policy.SignedBundle
	if err == nil {
		signed, err = policy.ReadSignedBundle(token)
	}
	unlock, lockErr := lockHome(g.home)
	if lockErr != nil {
		return g.status, lockErr
	}
	defer unlock()

	if bundleSizeError(err) {
		return g.reject(err)
	}
	current, configErr := readConfig(g.home)
	if configErr != nil {
		return g.reject(configErr)
	}
	g.config = current
	inForce := g.read()
	if err != nil {
		return g.reject(err)
	}
	candidate, action, err := g.admit(signed, inForce)
	if err != nil {
		return g.reject(err)
	}
	if err := writeJSON(g.home, policyFile, storedPolicy{Token: token, ConfirmedAt: g.now().UTC()}); err != nil {
		return g.status, err
	}
	g.read()
	return g.status, g.record(audit.Record{
		Action: action,
		Target: candidate.Bundle.ProjectID,
		Meta:   map[string]any{"version": candidate.Bundle.Version},
	})
}

func (g *Gate) reject(err error) (Status, error) {
	reason := err.Error()
	// A failed record does not join the rejection, because a caller takes
	// an error that wraps ErrNotRecorded to mean that the bundle is in force.
	_ = g.record(audit.Record{Action: "policy.rejected", Target: g.status.ProjectID, Reason: &reason})
	return g.status, fmt.Errorf("%w, and the policy in force stays: %w", ErrRejected, err)
}

// ErrNotRecorded is the error of a Load or a Decide that did its work and
// could not write the audit record of it.
var ErrNotRecorded = errors.New("the audit record was not written")

func (g *Gate) record(record audit.Record) error {
	if err := g.appendRecord(record); err != nil {
		return fmt.Errorf("%w: %w", ErrNotRecorded, err)
	}
	return nil
}

func (g *Gate) admit(signed *policy.SignedBundle, inForce *policy.Verified) (*policy.Verified, string, error) {
	keys, err := readKeys(g.home)
	if err != nil {
		return nil, "", fmt.Errorf("the key set cannot be read: %w", err)
	}
	if keys == nil {
		return nil, "", errors.New("no key set is configured")
	}
	candidate, err := g.verifier.Verify(signed, *keys)
	if err != nil {
		return nil, "", err
	}
	if err := g.config.checkBundle(candidate.Bundle); err != nil {
		return nil, "", err
	}
	floor, err := readFloor(g.home)
	if err != nil {
		return nil, "", fmt.Errorf("cannot read policy version floor (%s): %w", floorFile, err)
	}
	if floor == nil {
		floor = make(map[string]floorEntry)
		if err := g.seedFloor(floor, inForce); err != nil {
			return nil, "", err
		}
	}

	got := candidate.Bundle
	entry, err := floorEntryFor(got.Version, candidate.Payload)
	if err != nil {
		return nil, "", err
	}
	minimum, known := floor[got.ProjectID]
	if known {
		switch {
		case entry.Version < minimum.Version:
			return nil, "", fmt.Errorf("the bundle has version %d, older than version floor %d for project %q", entry.Version, minimum.Version, got.ProjectID)
		case entry.Version == minimum.Version && entry.SHA256 != minimum.SHA256:
			return nil, "", fmt.Errorf("the bundle matches version floor %d with different content for project %q", minimum.Version, got.ProjectID)
		}
	}

	action := "policy.loaded"
	if inForce != nil {
		have := inForce.Bundle
		switch {
		case got.ProjectID != have.ProjectID:
			return nil, "", errors.New("the bundle project_id must match the policy in force")
		case got.Version < have.Version:
			return nil, "", fmt.Errorf("the bundle has version %d, which is older than version %d in force", got.Version, have.Version)
		case got.Version == have.Version:
			if !samePayload(candidate, inForce) {
				return nil, "", fmt.Errorf("the bundle has version %d, as the policy in force has, with another content", got.Version)
			}
			action = "policy.confirmed"
		}
	}
	if !known || entry.Version > minimum.Version {
		floor[got.ProjectID] = entry
		// Publish the floor before policy.json so an interrupted load cannot
		// leave an enforced version ahead of its floor.
		if err := writeFloor(g.home, floor); err != nil {
			return nil, "", err
		}
	}
	return candidate, action, nil
}

func samePayload(a, b *policy.Verified) bool {
	canonicalA, errA := audit.Canonical(a.Payload)
	canonicalB, errB := audit.Canonical(b.Payload)
	return errA == nil && errB == nil && bytes.Equal(canonicalA, canonicalB)
}

// Verdict is the answer of the gate for one call of an agent.
type Verdict struct {
	// Policy is the state of the policy that the gate asked.
	Policy Status
	// Query is the query that decided: the first one that the policy
	// denied, or the first query of the call.
	Query policy.Query

	result  policy.Result
	decided bool
}

// Result returns the decision of the policy. It reports false when no
// verified policy is in force, and the caller then chooses what the agent
// may do. A Verdict has no zero decision that reads as allow or as deny.
func (v Verdict) Result() (policy.Result, bool) {
	return v.result, v.decided
}

// Decide evaluates the queries of one call of an agent and appends one
// audit record. agent names the coding agent, such as claude-code.
//
// The gate owns three fields of each query and replaces them: the member is
// the configured identity, the time is the clock of the gate, and the
// overrides are empty, pending a signed override contract. A read
// or a write with no repo is evaluated in every repo of the bundle. All the
// evaluations of the call share one budget. The first deny decides the call.
//
// The verdict is valid even when the error is not nil. The error wraps
// ErrNotRecorded.
func (g *Gate) Decide(agent string, first policy.Query, rest ...policy.Query) (Verdict, error) {
	queries := append([]policy.Query{first}, rest...)
	verdict := Verdict{Policy: g.status, Query: first}
	if g.status.Enforced() {
		verdict.decided = true
		verdict.Query, verdict.result = g.evaluate(queries)
	}
	unlock, err := lockHome(g.home)
	if err != nil {
		return verdict, fmt.Errorf("%w: %w", ErrNotRecorded, err)
	}
	defer unlock()
	return verdict, g.record(decisionRecord(agent, verdict, queries))
}

func (g *Gate) evaluate(queries []policy.Query) (policy.Query, policy.Result) {
	now := policy.TimestampOf(g.now())
	var firstQuery policy.Query
	var firstResult policy.Result
	budget := policy.NewBudget()
	for i, query := range queries {
		query.Member, query.Now, query.Overrides = g.config.MemberID, now, nil
		for _, repo := range g.candidateRepos(query) {
			query.Repo = repo
			result := budget.Evaluate(g.bundle, query)
			if result.Decision == policy.Deny {
				return query, result
			}
			if i == 0 && firstResult.Decision == "" {
				firstQuery, firstResult = query, result
			}
		}
	}
	return firstQuery, firstResult
}

// The caller may not know the repo of a path, so a read or a write with no
// repo must be allowed in every repo of the bundle.
func (g *Gate) candidateRepos(query policy.Query) []string {
	fileAction := query.Action == policy.Read || query.Action == policy.Write
	switch {
	case fileAction && g.config.ProjectID != "":
		return g.bundle.Repos
	case query.Repo != "" || !fileAction:
		return []string{query.Repo}
	case len(g.bundle.Repos) > 0:
		return g.bundle.Repos
	}
	return []string{"*"}
}
