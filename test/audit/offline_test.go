package audit_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/audit"
	"github.com/multiplexer-si/openplexer/internal/hub"
)

const checkedFive = "Chain is consistent from genesis (5 records). Completeness is proven through checkpoint count 5.\n"

func TestOfflineAudit(t *testing.T) {
	bin := buildBinaries(t, "openplexer-hub", "openplexer-verify")
	host, reviewer := t.TempDir(), t.TempDir()
	hostHome, reviewHome := t.TempDir(), t.TempDir()
	if err := os.Symlink(bin, filepath.Join(host, "bin")); err != nil {
		t.Fatal(err)
	}
	hubPath := filepath.Join(bin, "openplexer-hub")
	verifyPath := filepath.Join(bin, "openplexer-verify")
	admin := func(args ...string) {
		t.Helper()
		cmdArgs := append([]string{args[0], "--data-dir", "./hub-data"}, args[1:]...)
		exit, stdout, stderr := run(t, host, hostHome, bin, hubPath, cmdArgs...)
		if exit != 0 || stderr != "" {
			t.Fatalf("hub %v: exit %d, stdout %q, stderr %q", args, exit, stdout, stderr)
		}
	}
	verify := func(t *testing.T, wantExit int, wantOut, wantErr string, args ...string) {
		t.Helper()
		exit, stdout, stderr := run(t, reviewer, reviewHome, bin, verifyPath, args...)
		if exit != wantExit || stdout != wantOut || stderr != wantErr {
			t.Errorf("verify %v: exit %d, stdout %q, stderr %q\nwant exit %d, stdout %q, stderr %q", args, exit, stdout, stderr, wantExit, wantOut, wantErr)
		}
	}
	admin("init")
	admin("add-member", "--id", "mem-lee", "--role", "lead")
	admin("add-project", "--id", "prj-shop", "--repo", "api")
	admin("export-audit", "--out", "earlier-export")
	var bundle map[string]json.RawMessage
	decode(t, read(t, root+"/spec/policy/v1/examples/bundle/minimal.json"), &bundle)
	bundle["repos"] = json.RawMessage(`["api"]`)
	admin("publish-policy", "--project", "prj-shop", "--file", write(t, host, "bundle.json", encode(t, bundle)))

	guide := string(read(t, root+"/docs/audit.md"))
	hostExamples, reviewExamples, ok := strings.Cut(guide, "<!-- audit-example: reviewer -->")
	if !ok {
		t.Fatal("docs/audit.md is missing its reviewer marker")
	}
	runExamples(t, host, hostHome, bin, hostExamples)
	initial := filepath.Join(host, "audit-export")
	reviewExport := filepath.Join(reviewer, "audit-export")
	if err := os.Mkdir(reviewExport, 0700); err != nil {
		t.Fatal(err)
	}
	exportData := read(t, filepath.Join(initial, "export.json"))
	checkpointData := read(t, filepath.Join(initial, "checkpoint.jws"))
	oldExport := write(t, reviewExport, "export.json", exportData)
	oldCheckpoint := write(t, reviewExport, "checkpoint.jws", checkpointData)
	// The test operator takes its trusted copy directly from the local hub.
	jwksData := read(t, filepath.Join(initial, "jwks.json"))
	keys := write(t, reviewer, "trusted-jwks.json", jwksData)
	write(t, reviewer, "earlier-checkpoint.jws", read(t, filepath.Join(host, "earlier-export", "checkpoint.jws")))
	writeCheckpointFixtures(t, filepath.Join(reviewer, "checkpoint-fixtures"))
	runExamples(t, reviewer, reviewHome, bin, reviewExamples)

	admin("add-member", "--id", "mem-ana")
	admin("remove-member", "--id", "mem-ana")
	admin("export-audit", "--out", "later-export")
	later := filepath.Join(host, "later-export")
	newExport := write(t, reviewer, "later-export.json", read(t, filepath.Join(later, "export.json")))
	newCheckpoint := write(t, reviewer, "later-checkpoint.jws", read(t, filepath.Join(later, "checkpoint.jws")))
	verify(t, 0, "Chain is consistent from genesis (5 records). Completeness is proven through checkpoint count 3.\nRecords after position 3 are unanchored.\n", "",
		verifyArgs(newExport, keys, oldCheckpoint)...)
	verify(t, 0, checkedFive, "", verifyArgs(newExport, keys, oldCheckpoint, newCheckpoint)...)

	var original export
	decode(t, exportData, &original)
	if len(original.Entries) != 3 {
		t.Fatalf("the first export has %d entries, want 3", len(original.Entries))
	}
	var records [3]audit.Record
	for i, want := range []string{"policy.updated", "project.added", "member.added"} {
		decode(t, encode(t, original.Entries[i]), &records[i])
		if records[i].Action != want {
			t.Fatalf("entry %d action %q, want %q", i, records[i].Action, want)
		}
	}
	latest, middle, oldest := records[0], records[1], records[2]
	lastEntry, middleEntry, firstEntry := original.Entries[0], original.Entries[1], original.Entries[2]
	writeExport := func(name string, entries ...map[string]json.RawMessage) string {
		return write(t, reviewer, name+".json", encode(t, export{Entries: entries}))
	}
	changed := middle
	changed.Target = "project:changed"
	changed, err := audit.Seal(changed, middle.PrevHash)
	if err != nil {
		t.Fatal(err)
	}
	altered := maps.Clone(middleEntry)
	altered["target"] = encode(t, changed.Target)
	malformed := maps.Clone(middleEntry)
	malformed["flagged"] = json.RawMessage(`"false"`)
	brokenLink := fmt.Sprintf("record %s (position 2): rule A5: expected prev_hash %s, found %q. Check for missing or reordered records", latest.ID, oldest.Hash, latest.PrevHash)

	parts := strings.Split(string(checkpointData), ".")
	if len(parts) != 3 {
		t.Fatalf("the hub checkpoint has %d segments, want 3", len(parts))
	}
	var header struct{ Kid string }
	var claim audit.Checkpoint
	decode(t, decodeSegment(t, parts[0]), &header)
	decode(t, decodeSegment(t, parts[1]), &claim)
	badSig := write(t, reviewer, "bad-signature.jws", []byte(parts[0]+"."+parts[1]+"."+base64.RawURLEncoding.EncodeToString(make([]byte, 64))))
	policyJWKS := httptest.NewRecorder()
	hub.Open(filepath.Join(host, "hub-data")).Handler().ServeHTTP(policyJWKS, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
	if policyJWKS.Code != http.StatusOK {
		t.Fatalf("read policy JWKS: status %d, body %q", policyJWKS.Code, policyJWKS.Body)
	}
	policyKeys := write(t, reviewer, "policy-jwks.json", policyJWKS.Body.Bytes())

	// A second trusted signer makes it possible to exercise K5 and K8 with valid signatures.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const testKid = "audit-e2e-signer"
	var bothKeys struct {
		Keys []map[string]string `json:"keys"`
	}
	decode(t, jwksData, &bothKeys)
	bothKeys.Keys = append(bothKeys.Keys, map[string]string{
		"kty": "EC", "crv": "P-256", "kid": testKid,
		"x": base64.RawURLEncoding.EncodeToString(key.X.FillBytes(make([]byte, 32))),
		"y": base64.RawURLEncoding.EncodeToString(key.Y.FillBytes(make([]byte, 32))),
	})
	combinedKeys := write(t, reviewer, "combined-jwks.json", encode(t, bothKeys))
	unsupported, contradiction := claim, claim
	unsupported.Version++
	contradiction.TipHash = oldest.Hash
	badVersion := write(t, reviewer, "unsupported.jws", signCheckpoint(t, key, testKid, unsupported))
	otherTip := write(t, reviewer, "contradiction.jws", signCheckpoint(t, key, testKid, contradiction))

	for _, tc := range []struct {
		name, line string
		args       []string
	}{
		{"altered", fmt.Sprintf("record %s (position 2): rule A3: expected hash %s, found %q. Check the record's hashed fields", middle.ID, changed.Hash, middle.Hash),
			verifyArgs(writeExport("altered", lastEntry, altered, firstEntry), keys, oldCheckpoint)},
		{"reordered", brokenLink,
			verifyArgs(writeExport("reordered", middleEntry, lastEntry, firstEntry), keys, oldCheckpoint)},
		{"missing middle", brokenLink,
			verifyArgs(writeExport("missing-middle", lastEntry, firstEntry), keys, oldCheckpoint)},
		{"truncated tail", "record <none> (position 0): rule K9: checkpoint 1: expected at least 5 records, found 3. Check for a missing suffix or an older export",
			verifyArgs(oldExport, keys, newCheckpoint)},
		{"malformed record", fmt.Sprintf("record %s (position 2): rule A1: expected flagged to be a boolean, found a string. Export the log again", middle.ID),
			verifyArgs(writeExport("malformed", lastEntry, malformed, firstEntry), keys, oldCheckpoint)},
		{"unsupported version", "record <none> (position 0): rule K5: checkpoint 1: expected checkpoint version 1, found 2. Use a v1 audit checkpoint",
			verifyArgs(oldExport, combinedKeys, badVersion)},
		{"bad signature", "record <none> (position 0): rule K4: checkpoint 1: expected a valid ES256 signature, found a signature error (the signature does not verify under the key). Check the token and trusted key",
			verifyArgs(oldExport, keys, badSig)},
		{"policy keys", fmt.Sprintf("record <none> (position 0): rule K3: checkpoint 1: expected one public ES256 key for kid %q, found 0 matching keys. Check the trusted checkpoint JWKS", header.Kid),
			verifyArgs(oldExport, policyKeys, oldCheckpoint)},
		{"contradicting checkpoints", fmt.Sprintf("record <none> (position 0): rule K8: checkpoint 2: expected tip %s from checkpoint 1 at count 3, found %s. Compare independently kept checkpoints", latest.Hash, oldest.Hash),
			verifyArgs(oldExport, combinedKeys, oldCheckpoint, otherTip)},
	} {
		t.Run(tc.name, func(t *testing.T) { verify(t, 1, "", tc.line+"\n", tc.args...) })
	}
	validArgs := verifyArgs(oldExport, keys)
	for _, tc := range []struct {
		name, prefix string
		args         []string
	}{
		{"missing export flag", "Usage: openplexer-verify ", []string{"--jwks", keys}},
		{"missing JWKS flag", "Usage: openplexer-verify ", []string{"--export", oldExport}},
		{"extra argument", "Usage: openplexer-verify ", append(validArgs, "extra")},
		{"unknown flag", "flag provided but not defined: -unknown\n", []string{"--unknown"}},
		{"missing checkpoint value", "flag needs an argument: -checkpoint\n", append(validArgs, "--checkpoint")},
		{"unreadable export", "openplexer-verify: read export: ", verifyArgs("missing.json", keys)},
		{"unreadable JWKS", "openplexer-verify: read JWKS: ", verifyArgs(oldExport, "missing.json")},
		{"unreadable checkpoint", "openplexer-verify: read checkpoint: ", verifyArgs(oldExport, keys, "missing.jws")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exit, stdout, stderr := run(t, reviewer, reviewHome, bin, verifyPath, tc.args...)
			if exit != 2 || stdout != "" || !strings.HasPrefix(stderr, tc.prefix) {
				t.Errorf("exit %d, stdout %q, stderr %q, want exit 2 and stderr starting with %q", exit, stdout, stderr, tc.prefix)
			}
		})
	}
}

func verifyArgs(path, keys string, checkpoints ...string) []string {
	args := []string{"--export", path, "--jwks", keys}
	for _, token := range checkpoints {
		args = append(args, "--checkpoint", token)
	}
	return args
}

var exampleBlock = regexp.MustCompile("(?m)^```(sh|shell|bash|zsh|text)\\n([\\s\\S]*?)^```$")

func runExamples(t *testing.T, dir, home, bin, guide string) {
	t.Helper()
	blocks := exampleBlock.FindAllStringSubmatch(guide, -1)
	if len(blocks) == 0 {
		t.Fatal("docs/audit.md has no shell example for this directory")
	}
	normalize := strings.NewReplacer(dir, "<workdir>", home, "<home>", bin, "<bin>")
	for i := 0; i < len(blocks); i++ {
		if blocks[i][1] == "text" {
			t.Fatalf("docs/audit.md output has no preceding shell example: %q", blocks[i][2])
		}
		script, wantOut := blocks[i][2], ""
		if i+1 < len(blocks) && blocks[i+1][1] == "text" {
			i++
			wantOut = blocks[i][2]
		}
		exit, stdout, stderr := run(t, dir, home, bin, "/bin/sh", "-eu", "-c", script)
		if exit != 0 || normalize.Replace(stdout) != wantOut || stderr != "" {
			t.Fatalf("docs/audit.md command %q: exit %d, stdout %q, stderr %q, want exit 0 and stdout %q", script, exit, stdout, stderr, wantOut)
		}
	}
}

func writeCheckpointFixtures(t *testing.T, dir string) {
	t.Helper()
	var fixtures struct {
		Shared struct{ JWKS json.RawMessage }
		Cases  []struct {
			Name  string
			Input struct {
				Export      json.RawMessage
				Checkpoints []string
			}
		}
	}
	decode(t, read(t, auditSpec+"/fixtures/checkpoint.json"), &fixtures)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "jwks.json", fixtures.Shared.JWKS)
	for _, tc := range fixtures.Cases {
		caseDir := filepath.Join(dir, tc.Name)
		if err := os.Mkdir(caseDir, 0700); err != nil {
			t.Fatal(err)
		}
		write(t, caseDir, "export.json", tc.Input.Export)
		for i, token := range tc.Input.Checkpoints {
			write(t, caseDir, fmt.Sprintf("checkpoint-%d.jws", i+1), []byte(token))
		}
	}
}

func decodeSegment(t *testing.T, segment string) []byte {
	t.Helper()
	data, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func signCheckpoint(t *testing.T, key *ecdsa.PrivateKey, kid string, claim audit.Checkpoint) []byte {
	t.Helper()
	segment := func(data any) string {
		canonical, err := audit.Canonical(encode(t, data))
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(canonical)
	}
	input := segment(map[string]string{"alg": "ES256", "kid": kid, "typ": audit.CheckpointType}) + "." + segment(claim)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	var signature [64]byte
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return []byte(input + "." + base64.RawURLEncoding.EncodeToString(signature[:]))
}
