package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const specDir = "../../spec"

type fixtureFile struct {
	Operation string `json:"operation"`
	Shared    struct {
		JWKS json.RawMessage `json:"jwks"`
	} `json:"shared"`
	Cases []struct {
		Name     string          `json:"name"`
		Input    json.RawMessage `json:"input"`
		Expected json.RawMessage `json:"expected"`
	} `json:"cases"`
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func decode(t *testing.T, text []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(text, into); err != nil {
		t.Fatal(err)
	}
}

func runCanonicalize(t *testing.T, input, expected json.RawMessage) {
	var in struct {
		JSON *string `json:"json"`
	}
	var want struct {
		Canonical *string `json:"canonical"`
		SHA256    *string `json:"sha256"`
	}
	decode(t, input, &in)
	decode(t, expected, &want)
	if in.JSON == nil || want.Canonical == nil || want.SHA256 == nil {
		t.Fatal("the case has no json, canonical, or sha256")
	}
	got, err := Canonical([]byte(*in.JSON))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != *want.Canonical {
		t.Errorf("canonical = %s, want %s", got, *want.Canonical)
	}
	if sum := sha256.Sum256(got); hex.EncodeToString(sum[:]) != *want.SHA256 {
		t.Errorf("sha256 = %x, want %s", sum, *want.SHA256)
	}
}

func runVerifyChain(t *testing.T, input, expected json.RawMessage) {
	var in struct {
		Records json.RawMessage `json:"records"`
	}
	decode(t, input, &in)
	result, err := VerifyChain(in.Records)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	decode(t, encoded, &got)
	decode(t, expected, &want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("result = %s, want %s", encoded, expected)
	}
}

var operations = map[string]func(t *testing.T, input, expected json.RawMessage){
	"canonicalize": runCanonicalize,
	"verify-chain": runVerifyChain,
}

// Every case of every fixture file of the two contracts runs. A file with
// an operation that the table does not know fails, so that a new fixture
// file cannot go unread.
func TestFixtures(t *testing.T) {
	ran := 0
	for _, contract := range []string{"canonical-json", "audit"} {
		paths, err := filepath.Glob(filepath.Join(specDir, contract, "v1", "fixtures", "*.json"))
		if err != nil || len(paths) == 0 {
			t.Fatalf("found no fixture of %s: %v", contract, err)
		}
		for _, path := range paths {
			t.Run(filepath.Base(path), func(t *testing.T) {
				var file fixtureFile
				decode(t, readFile(t, path), &file)
				run, ok := operations[file.Operation]
				if file.Operation == "verify-checkpoints" {
					run, ok = checkpointRunner(t, file.Shared.JWKS), true
				}
				if !ok {
					t.Fatalf("unknown operation %q", file.Operation)
				}
				if len(file.Cases) == 0 {
					t.Fatal("no cases")
				}
				for _, c := range file.Cases {
					ran++
					t.Run(c.Name, func(t *testing.T) { run(t, c.Input, c.Expected) })
				}
			})
		}
	}
	t.Logf("ran %d cases", ran)
}

// The example records are the first three records of one chain, with
// hashes that the reference computed. Seal must give each record the hash
// that it has.
func TestSealGivesTheHashesOfTheExampleChain(t *testing.T) {
	previous := GenesisHash
	for _, name := range []string{"first", "second", "third"} {
		var example Record
		decode(t, readFile(t, filepath.Join(specDir, "audit", "v1", "examples", "record", name+".json")), &example)
		unsealed := example
		unsealed.PrevHash, unsealed.Hash = "", ""
		sealed, err := Seal(unsealed, previous)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(sealed, example) {
			t.Errorf("%s: sealed = %+v, want %+v", name, sealed, example)
		}
		previous = sealed.Hash
	}
}

func TestCanonicalRejectsTextThatIsNotJSON(t *testing.T) {
	for _, text := range []string{"", "{", "[1,]", "01", "'a'", "{} {}", "\"\xff\""} {
		if got, err := Canonical([]byte(text)); err == nil {
			t.Errorf("%q: canonical = %s, want an error", text, got)
		}
	}
}

func TestVerifyChainRejectsInputThatIsNotAListOfRecords(t *testing.T) {
	for _, text := range []string{`{}`, `[1]`, `[{"id": "1", "prev_hash": "", "hash": ""}]`, `[{"id": 1}]`} {
		if result, err := VerifyChain([]byte(text)); err == nil {
			t.Errorf("%s: result = %+v, want an error", text, result)
		}
	}
}
