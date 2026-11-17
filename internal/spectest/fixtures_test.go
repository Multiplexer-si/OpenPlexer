package spectest

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const fixtureFileID = idPrefix + "fixture-file"

// caseLabel names the case that a validation error is in, so that a failure
// points at a case by name and not by position.
func caseLabel(doc any, leaf *jsonschema.ValidationError) string {
	at := leaf.InstanceLocation
	if len(at) < 2 || at[0] != "cases" {
		return ""
	}
	index, err := strconv.Atoi(at[1])
	object, _ := doc.(map[string]any)
	cases, _ := object["cases"].([]any)
	if err != nil || index >= len(cases) {
		return ""
	}
	entry, _ := cases[index].(map[string]any)
	name, ok := entry["name"].(string)
	if !ok {
		return fmt.Sprintf("case %d: ", index)
	}
	return fmt.Sprintf("case %q: ", name)
}

// recheck recomputes the expected value of a case from the rules of the
// contract, for the two operations whose rules need only a hash. It returns
// one line for each difference.
func recheck(operation string, c map[string]any) []string {
	input, _ := c["input"].(map[string]any)
	expected, _ := c["expected"].(map[string]any)
	switch operation {
	case "canonicalize":
		if want := sha256Hex(expected["canonical"].(string)); expected["sha256"] != want {
			return []string{fmt.Sprintf("sha256 is %v, but the SHA-256 of canonical is %s", expected["sha256"], want)}
		}
	case "verify-chain":
		var problems []string
		records := input["records"].([]any)
		if got := verifyChain(records); !reflect.DeepEqual(got, expected) {
			problems = append(problems, fmt.Sprintf("the verification steps of spec/audit/README.md give %v, the fixture expects %v", got, expected))
		}
		informational, _ := c["informational"].(map[string]any)
		inputs, _ := informational["hash_inputs"].([]any)
		for i, text := range inputs {
			if i >= len(records) {
				problems = append(problems, fmt.Sprintf("hash_inputs has %d entries for %d records", len(inputs), len(records)))
				break
			}
			record := records[i].(map[string]any)
			if want := hashInput(record["prev_hash"].(string), record); text != want {
				problems = append(problems, fmt.Sprintf("hash_inputs[%d] is not prev_hash and the canonical JSON of the hashed fields of record %v, which is %s", i, record["id"], want))
			}
		}
		return problems
	}
	return nil
}

// A fixture file lives at spec/<contract>/v1/fixtures/<name>.json. The
// fixture-file schema fixes its shape. For every operation except validate,
// that schema also validates the input and the expected value of each case
// against the contract schemas. A validate case carries an input that may be
// invalid on purpose, so the test compares the verdict of the schema under
// test with the verdict that the fixture records. The schema under test must
// belong to the contract and the version of the file. A hash is recomputed
// where the rule that gives it needs no more than SHA-256.
func TestFixturesValidate(t *testing.T) {
	schemas := loadContracts(t)
	format, ok := schemas[fixtureFileID]
	if !ok {
		t.Fatalf("no schema %s", fixtureFileID)
	}
	files := filesUnder(t, func(rel string) bool {
		return strings.Contains(rel, "/fixtures/") && strings.HasSuffix(rel, ".json")
	})
	if len(files) == 0 {
		t.Fatal("found no fixture file under spec/")
	}
	total, verdicts, rechecked := 0, 0, 0
	for _, path := range files {
		doc := readJSON(t, path)
		if err := format.Validate(doc); err != nil {
			for _, leaf := range leaves(err) {
				t.Errorf("%s: %s%v", display(path), caseLabel(doc, leaf), leaf)
			}
			continue
		}
		file := doc.(map[string]any)
		rel, _ := filepath.Rel(specRoot, path)
		if dir, _, _ := strings.Cut(filepath.ToSlash(rel), "/"); file["contract"] != dir {
			t.Errorf("%s: contract is %v, but the file is under %s", display(path), file["contract"], dir)
		}
		operation := file["operation"].(string)
		var target *jsonschema.Schema
		if operation == "validate" {
			id := file["schema"].(string)
			if own := fmt.Sprintf("%s%v:v%v:", idPrefix, file["contract"], file["version"]); !strings.HasPrefix(id, own) {
				t.Errorf("%s: the schema under test is %s, but a fixture of this contract tests a schema whose $id starts with %s", display(path), id, own)
			}
			if target, ok = schemas[id]; !ok {
				t.Errorf("%s: no schema %s", display(path), id)
				continue
			}
		}
		seen := map[string]bool{}
		for _, entry := range file["cases"].([]any) {
			c := entry.(map[string]any)
			name := c["name"].(string)
			if seen[name] {
				t.Errorf("%s: case %q: the name is used twice", display(path), name)
			}
			seen[name] = true
			total++
			if operation == "canonicalize" || operation == "verify-chain" {
				rechecked++
			}
			for _, problem := range recheck(operation, c) {
				t.Errorf("%s: case %q: %s", display(path), name, problem)
			}
			if target == nil {
				continue
			}
			verdicts++
			want := c["expected"].(map[string]any)["valid"].(bool)
			if got := target.Validate(c["input"]) == nil; got != want {
				t.Errorf("%s: case %q: the schema %v gives valid=%v, the fixture expects valid=%v", display(path), name, file["schema"], got, want)
			}
		}
	}
	t.Logf("validated %d cases in %d fixture files; compared %d schema verdicts with the verdicts of the reference; recomputed the hashes of %d cases", total, len(files), verdicts, rechecked)
}
