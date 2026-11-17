package spectest

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"
)

// canonical writes a value that encoding/json decoded as the canonical JSON
// of spec/canonical-json/README.md. It exists so that the tests can recompute
// an audit hash. It is not the implementation of that contract: encoding/json
// has already replaced an unpaired surrogate and a repeated key before this
// function sees the value.
func canonical(value any) string {
	switch value := value.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(value)
	case float64:
		return canonicalNumber(value)
	case string:
		return canonicalString(value)
	case []any:
		parts := make([]string, len(value))
		for i, element := range value {
			parts[i] = canonical(element)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		slices.SortFunc(keys, func(a, b string) int {
			return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
		})
		parts := make([]string, len(keys))
		for i, key := range keys {
			parts[i] = canonicalString(key) + ":" + canonical(value[key])
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	panic("canonical: a value that encoding/json does not produce")
}

func canonicalNumber(n float64) string {
	switch size := math.Abs(n); {
	case n == 0:
		return "0"
	case math.IsInf(n, 0):
		return "null"
	case size >= 1e-6 && size < 1e21:
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	// Go writes at least two digits in an exponent, and ECMAScript writes no leading zero.
	mantissa, exponent, _ := strings.Cut(strconv.FormatFloat(n, 'e', -1, 64), "e")
	return mantissa + "e" + exponent[:1] + strings.TrimLeft(exponent[1:], "0")
}

func canonicalString(s string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			out.WriteByte('\\')
			out.WriteRune(r)
		case r >= 0x20:
			out.WriteRune(r)
		case strings.ContainsRune("\b\t\n\f\r", r):
			out.WriteByte('\\')
			out.WriteByte("btnfr"[strings.IndexRune("\b\t\n\f\r", r)])
		default:
			out.WriteString(`\u00` + hex.EncodeToString([]byte{byte(r)}))
		}
	}
	out.WriteByte('"')
	return out.String()
}

func sha256Hex(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

var hashedFields = []string{"workspace_id", "actor_member_id", "agent_session_id", "action", "target", "reason", "meta", "flagged", "created_at"}

const genesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// hashInput is the text whose SHA-256 is the hash of a record that follows a
// record with the hash prev: prev, then the canonical JSON of the nine hashed
// fields. spec/audit/README.md gives the rule.
func hashInput(prev string, record map[string]any) string {
	fields := map[string]any{}
	for _, name := range hashedFields {
		fields[name] = record[name]
	}
	return prev + canonical(fields)
}

// verifyChain follows the verification steps of spec/audit/README.md.
func verifyChain(records []any) map[string]any {
	prev := genesisHash
	for _, entry := range records {
		record := entry.(map[string]any)
		if record["prev_hash"] != prev || record["hash"] != sha256Hex(hashInput(prev, record)) {
			return map[string]any{"ok": false, "broken_at": record["id"]}
		}
		prev = record["hash"].(string)
	}
	return map[string]any{"ok": true, "entries": float64(len(records))}
}

func checkRecordHash(t *testing.T, where string, record map[string]any) {
	t.Helper()
	prev, _ := record["prev_hash"].(string)
	if want := sha256Hex(hashInput(prev, record)); record["hash"] != want {
		t.Errorf("%s: hash is %v, but the SHA-256 of prev_hash and the canonical JSON of the hashed fields is %s", where, record["hash"], want)
	}
}

var lastPartOfAction = regexp.MustCompile(`[^.-]*$`)

// The schemas cannot say that a hash is the right one. Every record among the
// examples must carry the hash that its own fields give, and the export
// example must be a chain that verifies once it is in chain order.
func TestAuditExamplesFollowTheHashRule(t *testing.T) {
	records := filesUnder(t, func(rel string) bool { return strings.HasPrefix(rel, "audit/v1/examples/record/") })
	exports := filesUnder(t, func(rel string) bool { return strings.HasPrefix(rel, "audit/v1/examples/export/") })
	if len(records) == 0 || len(exports) == 0 {
		t.Fatal("found no record example or no export example under spec/audit/")
	}
	for _, path := range records {
		checkRecordHash(t, display(path), readJSON(t, path).(map[string]any))
	}
	for _, path := range exports {
		entries := readJSON(t, path).(map[string]any)["entries"].([]any)
		for _, entry := range entries {
			record := entry.(map[string]any)
			action, _ := record["action"].(string)
			if want := lastPartOfAction.FindString(action); record["result"] != want {
				t.Errorf("%s: entry %v: result is %v, but the last part of the action %q is %q", display(path), record["id"], record["result"], action, want)
			}
		}
		slices.Reverse(entries)
		if result := verifyChain(entries); result["ok"] != true {
			t.Errorf("%s: the entries, oldest first, do not verify: the chain breaks at the entry %v", display(path), result["broken_at"])
		}
	}
	t.Logf("recomputed the hash of %d record examples and verified %d export examples", len(records), len(exports))
}
