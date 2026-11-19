package audit

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// VerifyResult is the outcome of checking an export and its checkpoints.
// Message is for human diagnostics. The JSON form follows the result schema.
type VerifyResult struct {
	OK                bool
	Entries           int
	CheckpointEntries *int
	Completeness      string
	Rule              string
	RecordNumber      int
	BrokenAt          json.RawMessage // JSON string when the record has a string id.
	Checkpoint        int
	Message           string
}

func (r VerifyResult) MarshalJSON() ([]byte, error) {
	if r.OK {
		return json.Marshal(struct {
			OK                bool   `json:"ok"`
			Entries           int    `json:"entries"`
			CheckpointEntries *int   `json:"checkpoint_entries"`
			Completeness      string `json:"completeness"`
		}{r.OK, r.Entries, r.CheckpointEntries, r.Completeness})
	}
	return json.Marshal(struct {
		OK           bool            `json:"ok"`
		Rule         string          `json:"rule"`
		RecordNumber int             `json:"record_number,omitempty"`
		BrokenAt     json.RawMessage `json:"broken_at,omitempty"`
		Checkpoint   int             `json:"checkpoint,omitempty"`
	}{r.OK, r.Rule, r.RecordNumber, r.BrokenAt, r.Checkpoint})
}

// Diagnostic describes the first failed check. Position 0 marks a failure
// that has no record location. An invalid or missing id is shown as <unknown>.
func (r VerifyResult) Diagnostic() string {
	id := "<none>"
	if r.RecordNumber > 0 {
		id = "<unknown>"
		if len(r.BrokenAt) > 0 {
			id = string(r.BrokenAt[1 : len(r.BrokenAt)-1])
			if id == "" {
				id = `""`
			}
		}
	}
	message := r.Message
	if r.Checkpoint > 0 {
		message = fmt.Sprintf("checkpoint %d: %s", r.Checkpoint, message)
	}
	return fmt.Sprintf("record %s (position %d): rule %s: %s", id, r.RecordNumber, r.Rule, message)
}

func (r VerifyResult) atRecord(record value, position int) VerifyResult {
	r.RecordNumber = position
	if id, _ := record.member("id"); id.kind == kindString {
		r.BrokenAt = id.appendCanonical(nil)
	}
	return r
}

// VerifyExport checks the newest-first JSON export from genesis, then checks
// the supplied tokens in order against a trusted checkpoint JWKS. A key set
// is only needed when a checkpoint is supplied.
func VerifyExport(export, jwks []byte, checkpoints ...string) VerifyResult {
	doc, err := parse(export)
	if err != nil || doc.kind != kindObject {
		return VerifyResult{Rule: "A6", Message: "expected one UTF-8 JSON object, found invalid export data. Check the file for corruption or trailing data"}
	}
	entries, _ := doc.member("entries")
	if entries.kind != kindArray {
		return VerifyResult{Rule: "A6", Message: "expected an entries array, found it missing or invalid. Export the log again"}
	}
	if cursor, present := doc.member("next_cursor"); !present || cursor.kind != kindNull {
		return VerifyResult{Rule: "A6", Message: "expected next_cursor to be null, found it missing or non-null. Export the full log without paging"}
	}
	if doc.duplicate != "" {
		return VerifyResult{Rule: "A6", Message: fmt.Sprintf("expected unique export keys, found repeated key %s. Export the log again", doc.duplicate)}
	}
	for _, field := range doc.members {
		if !slices.Equal(field.key, text("entries").units) {
			if key := field.value.duplicateKey(); key != "" {
				return VerifyResult{Rule: "A6", Message: fmt.Sprintf("expected unique export keys, found repeated key %s. Export the log again", key)}
			}
		}
	}

	slices.Reverse(entries.items)
	previous := GenesisHash
	for i, record := range entries.items {
		if message := checkEntry(record); message != "" {
			return (VerifyResult{Rule: "A1", Message: message}).atRecord(record, i+1)
		}
		prevHash, _ := record.member("prev_hash")
		if found, _ := prevHash.str(); found != previous {
			rule, hint := "A5", "Check for missing or reordered records"
			if i == 0 {
				rule, hint = "A4", "Check that the export starts at genesis and is unfiltered"
			}
			message := fmt.Sprintf("expected prev_hash %s, found %q. %s", previous, found, hint)
			return (VerifyResult{Rule: rule, Message: message}).atRecord(record, i+1)
		}
		computed, err := hashOf(previous, record)
		if err != nil {
			return (VerifyResult{Rule: "A1", Message: err.Error() + ". Export the log again"}).atRecord(record, i+1)
		}
		hash, _ := record.member("hash")
		if found, _ := hash.str(); found != computed {
			message := fmt.Sprintf("expected hash %s, found %q. Check the record's hashed fields", computed, found)
			return (VerifyResult{Rule: "A3", Message: message}).atRecord(record, i+1)
		}
		previous = computed
	}
	return verifyCheckpoints(entries.items, jwks, checkpoints)
}

func checkEntry(record value) string {
	if record.kind != kindObject {
		return fmt.Sprintf("expected a record object, found %s. Export the log again", record.kind)
	}
	if key := record.duplicateKey(); key != "" {
		return fmt.Sprintf("expected unique record keys, found repeated key %s. Export the log again", key)
	}
	for _, field := range []struct {
		name     string
		kind     kind
		nullable bool
		optional bool
	}{
		{"id", kindString, false, false},
		{"workspace_id", kindString, false, false},
		{"actor_member_id", kindString, true, false},
		{"agent_session_id", kindString, true, false},
		{"action", kindString, false, false},
		{"target", kindString, false, false},
		{"reason", kindString, true, false},
		{"meta", kindObject, false, false},
		{"flagged", kindBool, false, false},
		{"created_at", kindString, false, false},
		{"prev_hash", kindString, false, false},
		{"hash", kindString, false, false},
		{"result", kindString, false, false},
		{"agent_member_id", kindString, true, true},
	} {
		v, present := record.member(field.name)
		if !present && field.optional || present && (v.kind == field.kind || field.nullable && v.kind == kindNull) {
			continue
		}
		found := "missing"
		if present {
			found = v.kind.String()
		}
		expected := field.kind.String()
		if field.nullable {
			expected += " or null"
		}
		return fmt.Sprintf("expected %s to be %s, found %s. Export the log again", field.name, expected, found)
	}
	return ""
}

func (k kind) String() string {
	return [...]string{"null", "a boolean", "a number", "a string", "an array", "an object"}[k]
}

func (v value) duplicateKey() string {
	if v.duplicate != "" {
		return v.duplicate
	}
	for _, field := range v.members {
		if key := field.value.duplicateKey(); key != "" {
			return key
		}
	}
	for _, item := range v.items {
		if key := item.duplicateKey(); key != "" {
			return key
		}
	}
	return ""
}

// ActionResult derives the display result from the hashed action (A7).
func ActionResult(action string) string {
	return action[strings.LastIndexAny(action, ".-")+1:]
}
