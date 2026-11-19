package audit_test

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/audit"
)

const auditSpec = root + "/spec/audit/v1"

type chainResult struct {
	OK       bool   `json:"ok"`
	Entries  int    `json:"entries"`
	BrokenAt string `json:"broken_at"`
}

type export struct {
	Entries    []map[string]json.RawMessage `json:"entries"`
	NextCursor *string                      `json:"next_cursor"`
}

func TestReferenceCompatibility(t *testing.T) {
	bin := buildBinaries(t, "openplexer-verify")
	verify := filepath.Join(bin, "openplexer-verify")
	home := t.TempDir()
	dir := t.TempDir()
	jwks := []byte(`{"keys":[]}`)
	keys := write(t, dir, "jwks.json", jwks)
	check := func(t *testing.T, data []byte, want chainResult) {
		t.Helper()
		got := audit.VerifyExport(data, jwks)
		var id string
		if len(got.BrokenAt) > 0 {
			decode(t, got.BrokenAt, &id)
		}
		if (chainResult{got.OK, got.Entries, id}) != want {
			t.Errorf("VerifyExport = %+v, want reference result %+v", got, want)
		}

		path := write(t, dir, "export.json", data)
		exit, stdout, stderr := run(t, dir, home, bin, verify, "--export", path, "--jwks", keys, "--json")
		wantExit := 1
		if want.OK {
			wantExit = 0
		}
		if exit != wantExit || stderr != "" {
			t.Fatalf("exit %d, stdout %q, stderr %q, want exit %d and empty stderr", exit, stdout, stderr, wantExit)
		}
		var result chainResult
		decode(t, []byte(stdout), &result)
		if result != want {
			t.Errorf("openplexer-verify = %+v, want reference result %+v", result, want)
		}
	}

	var fixtures struct {
		Cases []struct {
			Name  string
			Input struct {
				Records []map[string]json.RawMessage
			}
			Expected chainResult
		}
	}
	decode(t, read(t, auditSpec+"/fixtures/verify-chain.json"), &fixtures)
	for _, fixture := range fixtures.Cases {
		t.Run(fixture.Name, func(t *testing.T) {
			check(t, referenceExport(t, fixture.Input.Records), fixture.Expected)
		})
	}

	const examples = auditSpec + "/examples/"
	var valid, broken chainResult
	decode(t, read(t, examples+"verify-result/valid.json"), &valid)
	decode(t, read(t, examples+"verify-result/broken.json"), &broken)
	t.Run("export example", func(t *testing.T) {
		check(t, read(t, examples+"export/three-records.json"), valid)
	})
	t.Run("record examples", func(t *testing.T) {
		records := make([]map[string]json.RawMessage, 3)
		for i, name := range []string{"first", "second", "third"} {
			decode(t, read(t, examples+"record/"+name+".json"), &records[i])
		}
		check(t, referenceExport(t, records), valid)
		// Match the target edit in tools/extract/examples.ts.
		records[1]["target"] = json.RawMessage(`"src/orders/cart.ts"`)
		check(t, referenceExport(t, records), broken)
	})
}

func referenceExport(t *testing.T, records []map[string]json.RawMessage) []byte {
	t.Helper()
	entries := make([]map[string]json.RawMessage, len(records))
	copy(entries, records)
	for _, entry := range entries {
		var action string
		decode(t, entry["action"], &action)
		entry["result"] = encode(t, audit.ActionResult(action))
		// The fixture has no session roster to resolve an owner.
		entry["agent_member_id"] = json.RawMessage(`null`)
	}
	slices.Reverse(entries)
	return encode(t, export{Entries: entries})
}
