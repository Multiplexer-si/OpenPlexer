package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/multiplexer-si/openplexer/internal/audit"
	"github.com/multiplexer-si/openplexer/internal/policy"
)

var decisionActions = map[policy.Decision]string{
	policy.Allow: "policy.allowed",
	policy.Deny:  "policy.denied",
}

// decisionRecord describes one verdict. With no verified policy the action
// is policy.unenforced, so that the log reports a missing policy.
func decisionRecord(agent string, verdict Verdict, queries []policy.Query) audit.Record {
	query := verdict.Query
	meta := map[string]any{
		"agent":        agent,
		"action":       string(query.Action),
		"policy_state": verdict.Policy.State.String(),
	}
	if query.Repo != "" {
		meta["repo"] = query.Repo
	}
	if len(queries) > 1 {
		targets := make([]string, len(queries))
		for i, q := range queries {
			targets[i] = target(q)
		}
		meta["targets"] = targets
	}
	record := audit.Record{
		Action:         "policy.unenforced",
		AgentSessionID: query.AgentSessionID,
		Target:         target(query),
		Reason:         &verdict.Policy.Detail,
		Meta:           meta,
	}
	if result, decided := verdict.Result(); decided {
		record.Action = decisionActions[result.Decision]
		record.Reason = &result.Rule
		meta["policy_version"] = verdict.Policy.Version
		if result.Area != nil {
			meta["area"] = *result.Area
		}
	}
	return record
}

// target names what a query acts on. A command can hold a secret, so the
// record holds its SHA-256 and not its text.
func target(query policy.Query) string {
	switch query.Action {
	case policy.Exec:
		sum := sha256.Sum256([]byte(query.Command))
		return "sha256:" + hex.EncodeToString(sum[:])
	case policy.GitPush:
		return query.Repo
	}
	return query.Path
}

// appendRecord adds one record to the audit log of the home. The caller
// holds the lock of the home. The gate fills the fields that say where and
// when: the log has no team, so workspace_id is the project of the policy
// in force.
func (g *Gate) appendRecord(record audit.Record) error {
	record.WorkspaceID = g.status.ProjectID
	if g.status.Member != "" {
		record.ActorMemberID = &g.status.Member
	}
	record.CreatedAt = g.now().UTC().Format(audit.CreatedAtLayout)

	file, err := os.OpenFile(filepath.Join(g.home, auditFile), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	last, err := lastRecord(file)
	if err != nil {
		return err
	}
	position, err := strconv.ParseUint(last.ID, 10, 64)
	if err != nil {
		return fmt.Errorf("the last record of %s has the id %q, which is not a position", auditFile, last.ID)
	}
	record.ID = strconv.FormatUint(position+1, 10)
	sealed, err := audit.Seal(record, last.Hash)
	if err != nil {
		return err
	}
	line, err := json.Marshal(sealed)
	if err != nil {
		return err
	}
	_, err = file.Write(append(line, '\n'))
	return err
}

type chainLink struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
}

const tailWindow = 1 << 20

// lastRecord returns the id and the hash of the last record of the log,
// or the start of a chain for an empty log. A writer that stopped in the
// middle of a line left a part of a record that no caller got an answer
// for, and lastRecord removes it.
func lastRecord(file *os.File) (chainLink, error) {
	info, err := file.Stat()
	if err != nil {
		return chainLink{}, err
	}
	start := max(0, info.Size()-tailWindow)
	tail := make([]byte, info.Size()-start)
	if _, err := file.ReadAt(tail, start); err != nil && !errors.Is(err, io.EOF) {
		return chainLink{}, err
	}
	if end := bytes.LastIndexByte(tail, '\n') + 1; end < len(tail) {
		if end == 0 && start > 0 {
			return chainLink{}, fmt.Errorf("the last record of %s is longer than %d bytes", auditFile, tailWindow)
		}
		if err := file.Truncate(start + int64(end)); err != nil {
			return chainLink{}, err
		}
		tail = tail[:end]
	}
	if len(tail) == 0 {
		return chainLink{ID: "0", Hash: audit.GenesisHash}, nil
	}
	line := tail[bytes.LastIndexByte(tail[:len(tail)-1], '\n')+1:]
	var last chainLink
	if err := json.Unmarshal(line, &last); err != nil || last.Hash == "" {
		return chainLink{}, fmt.Errorf("the last record of %s cannot be read, so the chain cannot continue", auditFile)
	}
	return last, nil
}
