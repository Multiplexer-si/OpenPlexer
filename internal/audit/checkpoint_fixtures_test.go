package audit

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const auditSchemaID = "urn:openplexer:spec:audit:v1:"

func TestMissingGenesisRuleAppliesToNonemptyExports(t *testing.T) {
	for _, path := range []string{
		filepath.Join(specDir, "audit", "README.md"),
		filepath.Join("..", "..", "docs", "trust-model.md"),
	} {
		if !bytes.Contains(readFile(t, path), []byte("nonempty filtered export that omits genesis")) {
			t.Errorf("%s must qualify the missing-genesis claim with nonempty", path)
		}
	}
}

type checkpointInput struct {
	Export      json.RawMessage `json:"export"`
	Checkpoints []string        `json:"checkpoints"`
	JWKSJSON    *string         `json:"jwks_json"`
}

func resultSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	const name = "checkpoint-verify-result"
	var doc any
	decode(t, readFile(t, filepath.Join(specDir, "audit", "v1", name+".schema.json")), &doc)
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(auditSchemaID+name, doc); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(auditSchemaID + name)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func checkpointRunner(t *testing.T, sharedJWKS json.RawMessage) func(*testing.T, json.RawMessage, json.RawMessage) {
	t.Helper()
	schema := resultSchema(t)
	return func(t *testing.T, input, expected json.RawMessage) {
		var in checkpointInput
		decode(t, input, &in)
		jwks := sharedJWKS
		if in.JWKSJSON != nil {
			jwks = json.RawMessage(*in.JWKSJSON)
		}
		got := VerifyExport(in.Export, jwks, in.Checkpoints...)
		result := checkResult(t, schema, got)
		var want any
		decode(t, expected, &want)
		if !reflect.DeepEqual(result, want) {
			t.Errorf("result = %v, want %s", result, expected)
		}
	}
}

func checkResult(t *testing.T, schema *jsonschema.Schema, result VerifyResult) any {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var wire any
	decode(t, encoded, &wire)
	if err := schema.Validate(wire); err != nil {
		t.Fatalf("result %s violates schema: %v", encoded, err)
	}
	if !result.OK {
		line := result.Diagnostic()
		if strings.ContainsAny(line, "\r\n") || !strings.Contains(line, "rule "+result.Rule+": ") || result.Message == "" {
			t.Errorf("incomplete diagnostic %q", line)
		}
	}
	return wire
}

func chainExportRunner(t *testing.T) func(*testing.T, json.RawMessage, json.RawMessage) {
	t.Helper()
	schema := resultSchema(t)
	return func(t *testing.T, input, expected json.RawMessage) {
		runVerifyChain(t, input, expected)

		var in struct {
			Records []map[string]json.RawMessage `json:"records"`
		}
		var want struct {
			OK       bool
			BrokenAt string `json:"broken_at"`
			Entries  int
		}
		decode(t, input, &in)
		decode(t, expected, &want)

		rule, position := "", 0
		previous := GenesisHash
		for i, record := range in.Records {
			var id, action, prevHash, hash string
			decode(t, record["id"], &id)
			decode(t, record["action"], &action)
			decode(t, record["prev_hash"], &prevHash)
			decode(t, record["hash"], &hash)
			record["result"], _ = json.Marshal(ActionResult(action))
			if !want.OK && id == want.BrokenAt && position == 0 {
				position = i + 1
				switch {
				case prevHash == previous:
					rule = "A3"
				case i == 0:
					rule = "A4"
				default:
					rule = "A5"
				}
			}
			previous = hash
		}
		slices.Reverse(in.Records)
		export, err := json.Marshal(map[string]any{"entries": in.Records, "next_cursor": nil})
		if err != nil {
			t.Fatal(err)
		}
		got := VerifyExport(export, nil)
		checkResult(t, schema, got)
		if got.OK != want.OK || got.Rule != rule || got.RecordNumber != position ||
			got.Entries != want.Entries || (!want.OK && string(got.BrokenAt) != `"`+want.BrokenAt+`"`) {
			t.Errorf("result = %+v, want chain result %+v, rule %s and position %d", got, want, rule, position)
		}
	}
}

func TestExportParsing(t *testing.T) {
	example := readFile(t, filepath.Join(specDir, "audit", "v1", "examples", "export", "three-records.json"))
	replace := func(before, after string) []byte {
		got := bytes.Replace(example, []byte(before), []byte(after), 1)
		if bytes.Equal(got, example) {
			t.Fatalf("cannot replace %q in the export example", before)
		}
		return got
	}
	for _, tc := range []struct {
		name, rule string
		data       []byte
	}{
		{"trailing value", "A6", append(bytes.Clone(example), []byte("{}")...)},
		{"invalid UTF-8", "A6", append(bytes.Clone(example), 0xff)},
		{"wrong entries case", "A6", replace(`"entries":`, `"Entries":`)},
		{"repeated entries", "A6", replace(`"entries":`, `"entries":[],"\u0065ntries":`)},
		{"nested export duplicate", "A6", replace(`"entries":`, `"extension":{"x":0,"x":1},"entries":`)},
		{"wrong action case", "A1", replace(`"action":`, `"Action":`)},
		{"duplicate record field", "A1", replace(`"action":`, `"action":"different","action":`)},
		{"duplicate meta field", "A1", replace(`"meta": {`, `"meta": {"x":[{"a":0,"a":1}],`)},
		{"invalid agent member", "A1", replace(`"agent_member_id": "mem-ana"`, `"agent_member_id": 17`)},
		{"missing id", "A1", replace(`"id":`, `"unknown_id":`)},
		{"extensions accepted", "", replace(`"action":`, `"v":2,"Action":"different","action":`)},
		{"display result isn't trusted", "", replace(`"result": "updated"`, `"result": "changed"`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := VerifyExport(tc.data, nil)
			if got.Rule != tc.rule || got.OK != (tc.rule == "") {
				t.Fatalf("VerifyExport = %+v, want rule %q", got, tc.rule)
			}
			if got.Rule == "A1" && (got.RecordNumber != 3 || (len(got.BrokenAt) == 0) != (tc.name == "missing id")) {
				t.Errorf("wrong record location: %+v", got)
			}
		})
	}
}

func TestCheckpointRejectsAmbiguousKeysAndNoncanonicalSegments(t *testing.T) {
	var vectors fixtureFile
	decode(t, readFile(t, filepath.Join(specDir, "audit", "v1", "fixtures", "checkpoint.json")), &vectors)
	for _, vector := range vectors.Cases {
		if vector.Name != "checkpoint at the tip" {
			continue
		}
		var in checkpointInput
		decode(t, vector.Input, &in)
		var keys struct{ Keys []json.RawMessage }
		decode(t, vectors.Shared.JWKS, &keys)
		token := in.Checkpoints[0]
		parts := strings.Split(token, ".")
		headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
		if err != nil {
			t.Fatal(err)
		}
		var header struct{ Kid string }
		decode(t, headerJSON, &header)
		for _, raw := range keys.Keys {
			var key map[string]any
			decode(t, raw, &key)
			if key["kid"] != header.Kid {
				continue
			}
			t.Run("duplicate kid even with an unusable key", func(t *testing.T) {
				jwks, err := json.Marshal(map[string]any{"keys": []any{key, map[string]any{"kid": header.Kid}}})
				if err != nil {
					t.Fatal(err)
				}
				if got := VerifyExport(in.Export, jwks, token); got.Rule != "K3" {
					t.Errorf("VerifyExport = %+v, want K3", got)
				}
			})
			for name, change := range map[string]func(map[string]any){
				"private":        func(k map[string]any) { k["d"] = nil },
				"spaces in x":    func(k map[string]any) { k["x"] = k["x"].(string) + "\n" },
				"extra key ops":  func(k map[string]any) { k["key_ops"] = []string{"verify", "sign"} },
				"different case": func(k map[string]any) { k["kid"] = strings.ToUpper(header.Kid) },
			} {
				t.Run(name, func(t *testing.T) {
					var key map[string]any
					decode(t, raw, &key)
					change(key)
					jwks, err := json.Marshal(map[string]any{"keys": []any{key}})
					if err != nil {
						t.Fatal(err)
					}
					if got := VerifyExport(in.Export, jwks, token); got.Rule != "K3" {
						t.Errorf("VerifyExport = %+v, want K3", got)
					}
				})
			}
		}
		for _, bad := range []string{token + "\n", parts[0] + ".AB." + parts[2], parts[0] + ".." + parts[2]} {
			if got := VerifyExport(in.Export, vectors.Shared.JWKS, bad); got.Rule != "K1" {
				t.Errorf("VerifyExport = %+v, want K1", got)
			}
		}
		return
	}
	t.Fatal("valid tip vector missing")
}
