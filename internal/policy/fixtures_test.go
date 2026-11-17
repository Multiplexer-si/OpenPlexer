package policy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const specDir = "../../spec/policy/v1"

type fixtureFile struct {
	Operation string `json:"operation"`
	Schema    string `json:"schema"`
	Shared    struct {
		Bundle json.RawMessage `json:"bundle"`
		JWKS   json.RawMessage `json:"jwks"`
	} `json:"shared"`
	Cases []fixtureCase `json:"cases"`
}

type fixtureCase struct {
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	Expected json.RawMessage `json:"expected"`
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func readFixture(t *testing.T, path string) fixtureFile {
	t.Helper()
	var file fixtureFile
	if err := json.Unmarshal(readFile(t, path), &file); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if len(file.Cases) == 0 {
		t.Fatalf("%s: no cases", path)
	}
	return file
}

func fixturePaths(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(specDir, "fixtures", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("found no fixture under %s: %v", specDir, err)
	}
	return paths
}

var validators = map[string]func(text []byte) error{
	"urn:openplexer:spec:policy:v1:bundle":   func(text []byte) error { _, err := ParseBundle(text); return err },
	"urn:openplexer:spec:policy:v1:override": func(text []byte) error { _, err := ParseOverride(text); return err },
}

func TestFixtures(t *testing.T) {
	ran := 0
	for _, path := range fixturePaths(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			file := readFixture(t, path)
			var run func(t *testing.T, c fixtureCase)
			switch file.Operation {
			case "validate":
				validate, ok := validators[file.Schema]
				if !ok {
					t.Fatalf("no validator for the schema %s", file.Schema)
				}
				run = func(t *testing.T, c fixtureCase) { runValidate(t, validate, c) }
			case "evaluate":
				bundle, err := ParseBundle(file.Shared.Bundle)
				if err != nil {
					t.Fatalf("shared bundle: %v", err)
				}
				run = func(t *testing.T, c fixtureCase) { runEvaluate(t, bundle, c) }
			case "verify-signed-bundle":
				keys, err := ParseKeySet(file.Shared.JWKS)
				if err != nil {
					t.Fatalf("shared jwks: %v", err)
				}
				run = func(t *testing.T, c fixtureCase) { runVerify(t, keys, c) }
			default:
				t.Fatalf("unknown operation %q", file.Operation)
			}
			for _, c := range file.Cases {
				ran++
				t.Run(c.Name, func(t *testing.T) { run(t, c) })
			}
		})
	}
	t.Logf("ran %d cases", ran)
}

func runValidate(t *testing.T, validate func([]byte) error, c fixtureCase) {
	var expected struct {
		Valid *bool `json:"valid"`
	}
	if err := json.Unmarshal(c.Expected, &expected); err != nil || expected.Valid == nil {
		t.Fatalf("expected has no valid: %v", err)
	}
	err := validate(c.Input)
	if valid := err == nil; valid != *expected.Valid {
		t.Fatalf("valid = %v, want %v (%v)", valid, *expected.Valid, err)
	}
	if err != nil {
		requireReasons(t, err)
	}
}

func requireReasons(t *testing.T, err error) {
	t.Helper()
	var invalid *ValidationError
	if !errors.As(err, &invalid) || len(invalid.Issues) == 0 {
		t.Fatalf("the error %q is not a ValidationError with an issue", err)
	}
	for _, issue := range invalid.Issues {
		if issue.Reason == "" {
			t.Errorf("the issue at %q has no reason", issue.Path)
		}
	}
}

func runEvaluate(t *testing.T, bundle *Bundle, c fixtureCase) {
	query, err := ParseQuery(c.Input)
	if err != nil {
		t.Fatalf("input: %v", err)
	}
	result := Evaluate(bundle, query)
	if result.Rule == "" {
		t.Errorf("the result %s has no rule", result.Decision)
	}
	text, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	if err := json.Unmarshal(text, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(c.Expected, &want); err != nil {
		t.Fatal(err)
	}
	delete(got, "rule")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("result = %v, want %v (rule: %s)", got, want, result.Rule)
	}
}

// A verified bundle is compared with expected as JSON, so that a field that
// Bundle does not model must survive too.
func runVerify(t *testing.T, keys KeySet, c fixtureCase) {
	var input struct {
		Signature *string `json:"signature"`
	}
	if err := json.Unmarshal(c.Input, &input); err != nil || input.Signature == nil {
		t.Fatalf("input has no signature: %v", err)
	}
	var want map[string]any
	if err := json.Unmarshal(c.Expected, &want); err != nil {
		t.Fatal(err)
	}
	verified, err := VerifySignedBundle(*input.Signature, keys)
	got := map[string]any{"ok": err == nil}
	if err != nil {
		if err.Error() == "" {
			t.Error("the error has no reason")
		}
	} else {
		var bundle any
		if err := json.Unmarshal(verified.Payload, &bundle); err != nil {
			t.Fatalf("payload: %v", err)
		}
		got["bundle"] = bundle
		if verified.Bundle == nil {
			t.Error("the verified result has no bundle")
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("result = %v, want %v (%v)", got, want, err)
	}
}
