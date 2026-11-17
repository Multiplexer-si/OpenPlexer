package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// GenesisHash is the prev_hash of the first record of a chain.
var GenesisHash = strings.Repeat("0", 64)

// Record is one record of spec/audit/v1/record.schema.json.
type Record struct {
	ID             string         `json:"id"`
	WorkspaceID    string         `json:"workspace_id"`
	ActorMemberID  *string        `json:"actor_member_id"`
	AgentSessionID *string        `json:"agent_session_id"`
	Action         string         `json:"action"`
	Target         string         `json:"target"`
	Reason         *string        `json:"reason"`
	Meta           map[string]any `json:"meta"`
	Flagged        bool           `json:"flagged"`
	CreatedAt      string         `json:"created_at"`
	PrevHash       string         `json:"prev_hash"`
	Hash           string         `json:"hash"`
}

// CreatedAtLayout is the form of created_at that the reference writes: UTC
// with milliseconds.
const CreatedAtLayout = "2006-01-02T15:04:05.000Z"

var hashedFields = []string{"workspace_id", "actor_member_id", "agent_session_id", "action", "target", "reason", "meta", "flagged", "created_at"}

// Seal returns the record as the next record of a chain whose last hash is
// prevHash. It sets PrevHash and Hash.
func Seal(r Record, prevHash string) (Record, error) {
	if r.Meta == nil {
		r.Meta = map[string]any{}
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		return Record{}, fmt.Errorf("the record cannot be written as JSON: %w", err)
	}
	fields, err := parse(encoded)
	if err != nil {
		return Record{}, err
	}
	r.PrevHash = prevHash
	r.Hash, err = hashOf(prevHash, fields)
	return r, err
}

// hashOf follows the steps of spec/audit/README.md, section "The hash of a
// record".
func hashOf(prevHash string, record value) (string, error) {
	members := make([]member, len(hashedFields))
	for i, name := range hashedFields {
		field, ok := record.member(name)
		if !ok {
			return "", fmt.Errorf("the record has no %s", name)
		}
		members[i] = member{key: text(name).units, value: field}
	}
	sum := sha256.Sum256(object(members...).appendCanonical([]byte(prevHash)))
	return hex.EncodeToString(sum[:]), nil
}

// ChainResult marshals to spec/audit/v1/verify-result.schema.json. Entries
// counts the records of a valid chain. BrokenAt is the id of the first
// record that does not follow from the record before it.
type ChainResult struct {
	OK       bool
	Entries  int
	BrokenAt string
}

func (r ChainResult) MarshalJSON() ([]byte, error) {
	if r.OK {
		return json.Marshal(map[string]any{"ok": true, "entries": r.Entries})
	}
	return json.Marshal(map[string]any{"ok": false, "broken_at": r.BrokenAt})
}

// VerifyChain verifies the records of one log, oldest first, under the
// steps of spec/audit/README.md. The input is the JSON text of the list of
// records. The error is for an input that is not a list of records.
func VerifyChain(records []byte) (ChainResult, error) {
	list, err := parse(records)
	if err != nil {
		return ChainResult{}, err
	}
	if list.kind != kindArray {
		return ChainResult{}, errors.New("the input is not a list of records")
	}
	expected := GenesisHash
	for i, record := range list.items {
		var linked [3]string
		for j, name := range []string{"id", "prev_hash", "hash"} {
			field, _ := record.member(name)
			var ok bool
			if linked[j], ok = field.str(); !ok {
				return ChainResult{}, fmt.Errorf("record %d has no %s that is a string", i, name)
			}
		}
		id, prevHash, hash := linked[0], linked[1], linked[2]
		computed, err := hashOf(expected, record)
		if err != nil {
			return ChainResult{}, fmt.Errorf("record %d: %w", i, err)
		}
		if prevHash != expected || hash != computed {
			return ChainResult{BrokenAt: id}, nil
		}
		expected = hash
	}
	return ChainResult{OK: true, Entries: len(list.items)}, nil
}
