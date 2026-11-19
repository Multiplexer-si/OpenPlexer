package daemon

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/multiplexer-si/openplexer/internal/audit"
	"github.com/multiplexer-si/openplexer/internal/policy"
)

const specDir = "../../spec"

var loadedAt = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

func at(instant time.Time) func() time.Time {
	return func() time.Time { return instant }
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

type fixtureFile struct {
	Operation string `json:"operation"`
	Shared    struct {
		Bundle json.RawMessage `json:"bundle"`
		JWKS   json.RawMessage `json:"jwks"`
	} `json:"shared"`
	Cases []fixtureCase `json:"cases"`
}

type fixtureCase struct {
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	Expected map[string]any  `json:"expected"`
}

type caseInput struct {
	Signature string          `json:"signature"`
	Member    string          `json:"member"`
	Now       time.Time       `json:"now"`
	Overrides json.RawMessage `json:"overrides"`
}

func (c fixtureCase) input(t *testing.T) caseInput {
	t.Helper()
	var input caseInput
	decode(t, c.Input, &input)
	return input
}

func readFixture(t *testing.T, name string) fixtureFile {
	t.Helper()
	var file fixtureFile
	decode(t, readFile(t, filepath.Join(specDir, "policy", "v1", "fixtures", name)), &file)
	if len(file.Cases) == 0 {
		t.Fatalf("%s: no cases", name)
	}
	return file
}

func (f fixtureFile) token(t *testing.T, name string) string {
	t.Helper()
	for _, c := range f.Cases {
		if c.Name == name {
			return c.input(t).Signature
		}
	}
	t.Fatalf("no case %q", name)
	return ""
}

type tokenSource string

func (token tokenSource) Fetch(context.Context) (string, error) { return string(token), nil }

func configure(t *testing.T, home string, change Settings) {
	t.Helper()
	if err := Configure(home, change); err != nil {
		t.Fatal(err)
	}
}

func open(t *testing.T, home string, now time.Time) *Gate {
	t.Helper()
	gate, err := Open(home, at(now))
	if err != nil {
		t.Fatal(err)
	}
	return gate
}

func load(t *testing.T, gate *Gate, source Source) Status {
	t.Helper()
	status, err := gate.Load(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

// signer signs bundles with a key that the test makes, for the cases that
// need a bundle that no fixture holds signed.
type signer struct {
	key *ecdsa.PrivateKey
}

func newSigner(t *testing.T) signer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return signer{key}
}

func (s signer) keySet(t *testing.T) []byte {
	t.Helper()
	point, err := s.key.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	return fmt.Appendf(nil, `{"keys": [{"kty": "EC", "crv": "P-256", "kid": "test", "x": %q, "y": %q}]}`, encode(point[1:33]), encode(point[33:]))
}

func (s signer) sign(t *testing.T, bundle []byte) tokenSource {
	t.Helper()
	encode := base64.RawURLEncoding.EncodeToString
	input := encode([]byte(`{"alg":"ES256","typ":"mp-policy+jws","kid":"test"}`)) + "." + encode(bundle)
	return s.signInput(t, input)
}

func (s signer) signInput(t *testing.T, input string) tokenSource {
	t.Helper()
	digest := sha256.Sum256([]byte(input))
	r, ss, err := ecdsa.Sign(rand.Reader, s.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	signature := append(r.FillBytes(make([]byte, 32)), ss.FillBytes(make([]byte, 32))...)
	return tokenSource(input + "." + base64.RawURLEncoding.EncodeToString(signature))
}

func bundleText(project string, version int) []byte {
	return fmt.Appendf(nil, `{"version": %d, "project_id": %q, "repos": ["api"], "areas": [{"name": "backend", "patterns": ["api:src/**"]}], "roles": {"backend": {"read": [], "write": ["backend"], "exec": []}}, "members": {"mem-ana": "backend"}}`, version, project)
}

func auditRecords(t *testing.T, home string) []audit.Record {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(string(readFile(t, filepath.Join(home, auditFile))), "\n"), "\n")
	result, err := audit.VerifyChain([]byte("[" + strings.Join(lines, ",") + "]"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Entries != len(lines) {
		t.Fatalf("the audit log is not a valid chain of %d records: %+v", len(lines), result)
	}
	records := make([]audit.Record, len(lines))
	for i, line := range lines {
		decode(t, []byte(line), &records[i])
	}
	return records
}

func actions(records []audit.Record) []string {
	out := make([]string, len(records))
	for i, record := range records {
		out[i] = record.Action
	}
	return out
}

func decisionOf(t *testing.T, verdict Verdict) map[string]any {
	t.Helper()
	result, decided := verdict.Result()
	if !decided {
		t.Fatalf("no policy decided: %s", verdict.Policy.Detail)
	}
	if result.Rule == "" {
		t.Error("the result has no rule")
	}
	text, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	decode(t, text, &got)
	delete(got, "rule")
	return got
}

// The signed bundle example carries the bundle that the eval-result
// examples were computed against. The gate, with the example key set and
// the example message on disk, must give each of those results.
func TestGateEnforcesTheSignedBundleExample(t *testing.T) {
	examples := filepath.Join(specDir, "policy", "v1", "examples")
	home := t.TempDir()
	configure(t, home, Settings{KeySet: readFile(t, filepath.Join(examples, "jwks", "hub-keys.json")), MemberID: "mem-ana"})
	status := load(t, open(t, home, loadedAt), FileSource(filepath.Join(examples, "signed-bundle", "message.json")))
	if status.State != Verified || status.Role != "backend" {
		t.Fatalf("status = %+v, want a verified policy with the role backend", status)
	}
	queries, err := filepath.Glob(filepath.Join(examples, "eval-query", "*.json"))
	if err != nil || len(queries) == 0 {
		t.Fatalf("found no eval-query example: %v", err)
	}
	ran := 0
	for _, path := range queries {
		name := filepath.Base(path)
		var input struct {
			Member    string          `json:"member"`
			Now       time.Time       `json:"now"`
			Overrides json.RawMessage `json:"overrides"`
		}
		decode(t, readFile(t, path), &input)
		if input.Overrides != nil {
			continue
		}
		if input.Member != "mem-ana" {
			t.Fatalf("%s: the member is %s, and this test configures mem-ana", name, input.Member)
		}
		query, err := policy.ParseQuery(readFile(t, path))
		if err != nil {
			t.Fatal(err)
		}
		verdict, err := open(t, home, input.Now).Decide("claude-code", query)
		if err != nil {
			t.Fatal(err)
		}
		var want map[string]any
		decode(t, readFile(t, filepath.Join(examples, "eval-result", name)), &want)
		delete(want, "rule")
		if got := decisionOf(t, verdict); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: result = %v, want %v", name, got, want)
		}
		ran++
	}
	if ran < 4 {
		t.Fatalf("ran %d examples, want at least 4", ran)
	}
	if got := len(auditRecords(t, home)); got != ran+1 {
		t.Errorf("the audit log has %d records, want one for the load and one for each of the %d decisions", got, ran)
	}
}

// Every evaluate case that has no override runs through the gate: the
// shared bundle is signed and loaded, the member of the case is the local
// identity, and the time of the case is the clock. The decision and the
// area must be the ones that the reference gave. Where the reference throws,
// the gate must deny with no area. A case with overrides is
// left out, because the Phase 1 gate has no source of overrides.
func TestGateGivesTheResultOfEveryEvaluateFixture(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(specDir, "policy", "v1", "fixtures", "evaluate-*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("found no evaluate fixture: %v", err)
	}
	keys := newSigner(t)
	ran, withOverrides := 0, 0
	for _, path := range paths {
		file := readFixture(t, filepath.Base(path))
		token := keys.sign(t, file.Shared.Bundle)
		homes := map[string]string{}
		for _, c := range file.Cases {
			input := c.input(t)
			if input.Overrides != nil {
				withOverrides++
				continue
			}
			home, ok := homes[input.Member]
			if !ok {
				home = t.TempDir()
				homes[input.Member] = home
				configure(t, home, Settings{KeySet: keys.keySet(t), MemberID: input.Member})
				load(t, open(t, home, loadedAt), token)
			}
			query, err := policy.ParseQuery(c.Input)
			if err != nil {
				t.Fatalf("%s: %s: %v", filepath.Base(path), c.Name, err)
			}
			verdict, err := open(t, home, input.Now).Decide("codex", query)
			if err != nil {
				t.Fatal(err)
			}
			want := c.Expected
			if want["throws"] == true {
				want = map[string]any{"decision": "deny"}
			}
			if got := decisionOf(t, verdict); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: %s: result = %v, want %v", filepath.Base(path), c.Name, got, want)
			}
			ran++
		}
	}
	if ran == 0 {
		t.Fatal("ran no case")
	}
	t.Logf("ran %d cases through the gate, and left out %d cases with overrides", ran, withOverrides)
}

// The gate must accept exactly the tokens that the two verify fixtures
// accept. A rejected token must leave the policy in force as it was.
func TestLoadAcceptsExactlyTheBundlesThatVerify(t *testing.T) {
	accepted, rejected := 0, 0
	for _, name := range []string{"verify-signed-bundle.json", "verify-signed-bundle-key-selection.json"} {
		file := readFixture(t, name)
		for _, c := range file.Cases {
			home := t.TempDir()
			configure(t, home, Settings{KeySet: file.Shared.JWKS})
			gate := open(t, home, loadedAt)
			status, err := gate.Load(context.Background(), tokenSource(c.input(t).Signature))
			if c.Expected["ok"] == true {
				accepted++
				if err != nil || status.State != Verified {
					t.Errorf("%s: %s: status = %+v, error = %v, want a verified policy", name, c.Name, status, err)
				}
				continue
			}
			rejected++
			if !errors.Is(err, ErrRejected) || status.State != Absent || open(t, home, loadedAt).Status().State != Absent {
				t.Errorf("%s: %s: status = %+v, error = %v, want a rejected bundle and no policy", name, c.Name, status, err)
			}
			if got := actions(auditRecords(t, home)); !reflect.DeepEqual(got, []string{"policy.rejected"}) {
				t.Errorf("%s: %s: audit actions = %v, want one policy.rejected", name, c.Name, got)
			}
		}
	}
	if accepted == 0 || rejected == 0 {
		t.Fatalf("accepted %d and rejected %d, so the test compares one verdict only", accepted, rejected)
	}
	t.Logf("accepted %d tokens and rejected %d", accepted, rejected)
}

func TestARejectedBundleKeepsThePolicyInForce(t *testing.T) {
	file := readFixture(t, "verify-signed-bundle.json")
	home := t.TempDir()
	configure(t, home, Settings{KeySet: file.Shared.JWKS, MemberID: "mem-ana"})
	gate := open(t, home, loadedAt)
	before := load(t, gate, tokenSource(file.token(t, "a bundle signed by a key in the key set verifies")))
	stored := readFile(t, filepath.Join(home, policyFile))

	for _, name := range []string{"a payload changed after signing does not verify", "a signature from another key with the same kid does not verify"} {
		status, err := gate.Load(context.Background(), tokenSource(file.token(t, name)))
		if !errors.Is(err, ErrRejected) {
			t.Errorf("%s: error = %v, want a rejected bundle", name, err)
		}
		if !reflect.DeepEqual(status, before) || string(readFile(t, filepath.Join(home, policyFile))) != string(stored) {
			t.Errorf("%s: the policy in force changed: %+v", name, status)
		}
	}
	verdict, err := open(t, home, loadedAt).Decide("claude-code", policy.Query{Action: policy.Write, Repo: "api", Path: "src/a.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if _, decided := verdict.Result(); !decided {
		t.Errorf("the gate stopped deciding after a rejected bundle: %+v", verdict.Policy)
	}
	if got, want := actions(auditRecords(t, home)), []string{"policy.loaded", "policy.rejected", "policy.rejected"}; !reflect.DeepEqual(got[:3], want) {
		t.Errorf("audit actions = %v, want %v first", got, want)
	}
}

func TestAPolicyGoesStaleAndKeepsDeciding(t *testing.T) {
	file := readFixture(t, "verify-signed-bundle.json")
	token := tokenSource(file.token(t, "a bundle signed by a key in the key set verifies"))
	home := t.TempDir()
	configure(t, home, Settings{KeySet: file.Shared.JWKS, MemberID: "mem-ana", StaleAfterDays: 3})
	load(t, open(t, home, loadedAt), token)

	lastFreshMoment := loadedAt.Add(3*24*time.Hour - time.Millisecond)
	if status := open(t, home, lastFreshMoment).Status(); status.State != Verified {
		t.Errorf("just before three days: status = %+v, want verified", status)
	}
	staleAt := loadedAt.Add(3 * 24 * time.Hour)
	gate := open(t, home, staleAt)
	if status := gate.Status(); status.State != Stale || !status.StaleAt.Equal(staleAt) {
		t.Fatalf("after three days: status = %+v, want stale since %s", status, staleAt)
	}
	query := policy.Query{Action: policy.Write, Repo: "api", Path: "src/a.ts"}
	verdict, err := gate.Decide("claude-code", query)
	if err != nil {
		t.Fatal(err)
	}
	if _, decided := verdict.Result(); !decided || verdict.Policy.State != Stale {
		t.Errorf("a stale policy must decide and report that it is stale: %+v", verdict)
	}
	records := auditRecords(t, home)
	if last := records[len(records)-1]; last.Meta["policy_state"] != "stale" {
		t.Errorf("the audit record of the decision has policy_state %v, want stale", last.Meta["policy_state"])
	}

	if status := load(t, gate, token); status.State != Verified || !status.ConfirmedAt.Equal(staleAt) {
		t.Errorf("after the source confirmed the bundle: status = %+v, want verified and confirmed at %s", status, staleAt)
	}
	records = auditRecords(t, home)
	if last := records[len(records)-1]; last.Action != "policy.confirmed" {
		t.Errorf("the last audit action is %s, want policy.confirmed", last.Action)
	}
}

func TestAnUnavailableSourceKeepsThePolicyInForce(t *testing.T) {
	file := readFixture(t, "verify-signed-bundle.json")
	home := t.TempDir()
	configure(t, home, Settings{KeySet: file.Shared.JWKS, MemberID: "mem-ana"})
	gate := open(t, home, loadedAt)
	before := load(t, gate, tokenSource(file.token(t, "a bundle signed by a key in the key set verifies")))

	for name, source := range map[string]Source{
		"a file that does not exist":   FileSource(filepath.Join(home, "no-such-bundle.json")),
		"a file that is not a message": FileSource(filepath.Join(home, keysFile)),
	} {
		status, err := gate.Load(context.Background(), source)
		if err == nil || errors.Is(err, ErrRejected) || !reflect.DeepEqual(status, before) {
			t.Errorf("%s: status = %+v, error = %v, want the policy in force and an error that is not a rejection", name, status, err)
		}
	}
	verdict, err := open(t, home, loadedAt.Add(time.Hour)).Decide("claude-code", policy.Query{Action: policy.Write, Repo: "api", Path: "src/a.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if _, decided := verdict.Result(); !decided {
		t.Errorf("the gate stopped deciding when its source went away: %+v", verdict.Policy)
	}
}

func TestLoadRefusesAnOlderVersionAndAnotherProject(t *testing.T) {
	keys := newSigner(t)
	home := t.TempDir()
	configure(t, home, Settings{KeySet: keys.keySet(t)})
	gate := open(t, home, loadedAt)
	load(t, gate, keys.sign(t, bundleText("prj-shop", 7)))

	sameBundleOtherForm := append([]byte("  "), bundleText("prj-shop", 7)...)
	otherContent := []byte(strings.Replace(string(bundleText("prj-shop", 7)), "src/**", "**", 1))
	steps := []struct {
		name        string
		bundle      []byte
		wantVersion int64
		wantAction  string
	}{
		{"an older version of the same project", bundleText("prj-shop", 6), 7, "policy.rejected"},
		{"the same version with another content", otherContent, 7, "policy.rejected"},
		{"a bundle for another project", bundleText("prj-other", 9), 7, "policy.rejected"},
		{"the same bundle under a new signature", bundleText("prj-shop", 7), 7, "policy.confirmed"},
		{"the same bundle in another JSON form", sameBundleOtherForm, 7, "policy.confirmed"},
		{"a newer version", bundleText("prj-shop", 8), 8, "policy.loaded"},
		{"the version that was in force before", bundleText("prj-shop", 7), 8, "policy.rejected"},
	}
	for _, step := range steps {
		status, err := gate.Load(context.Background(), keys.sign(t, step.bundle))
		if rejected := step.wantAction == "policy.rejected"; errors.Is(err, ErrRejected) != rejected || (err != nil) != rejected {
			t.Errorf("%s: error = %v, want rejected = %v", step.name, err, rejected)
		}
		if status.State != Verified || status.Version != step.wantVersion || status.ProjectID != "prj-shop" {
			t.Errorf("%s: status = %+v, want version %d of prj-shop in force", step.name, status, step.wantVersion)
		}
		records := auditRecords(t, home)
		if last := records[len(records)-1]; last.Action != step.wantAction {
			t.Errorf("%s: the audit action is %s, want %s", step.name, last.Action, step.wantAction)
		}
	}
}

func TestAStoredPolicyThatNoLongerVerifiesIsNotEnforced(t *testing.T) {
	file := readFixture(t, "verify-signed-bundle.json")
	token := file.token(t, "a bundle signed by a key in the key set verifies")
	otherKeys := newSigner(t).keySet(t)
	query := policy.Query{Action: policy.Write, Repo: "api", Path: "src/a.ts"}

	for name, c := range map[string]struct {
		damage func(t *testing.T, home string)
		want   State
	}{
		"the stored token is edited": {func(t *testing.T, home string) {
			path := filepath.Join(home, policyFile)
			edited := strings.Replace(string(readFile(t, path)), token, file.token(t, "a payload changed after signing does not verify"), 1)
			if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
				t.Fatal(err)
			}
		}, Rejected},
		"the stored policy is not JSON": {func(t *testing.T, home string) {
			if err := os.WriteFile(filepath.Join(home, policyFile), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, Rejected},
		"the key set no longer holds the key": {func(t *testing.T, home string) {
			configure(t, home, Settings{KeySet: otherKeys})
		}, Rejected},
		"the key set is removed": {func(t *testing.T, home string) {
			if err := os.Remove(filepath.Join(home, keysFile)); err != nil {
				t.Fatal(err)
			}
		}, Rejected},
		"the stored policy is removed": {func(t *testing.T, home string) {
			if err := os.Remove(filepath.Join(home, policyFile)); err != nil {
				t.Fatal(err)
			}
		}, Absent},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			configure(t, home, Settings{KeySet: file.Shared.JWKS, MemberID: "mem-ana"})
			load(t, open(t, home, loadedAt), tokenSource(token))
			c.damage(t, home)

			gate := open(t, home, loadedAt)
			if status := gate.Status(); status.State != c.want || status.Detail == "" {
				t.Fatalf("status = %+v, want %s with a detail", status, c.want)
			}
			verdict, err := gate.Decide("claude-code", query)
			if err != nil {
				t.Fatal(err)
			}
			if result, decided := verdict.Result(); decided {
				t.Errorf("the gate decided %+v with no verified policy", result)
			}
			records := auditRecords(t, home)
			last := records[len(records)-1]
			if last.Action != "policy.unenforced" || last.Reason == nil || *last.Reason != gate.Status().Detail {
				t.Errorf("the audit record is %+v, want policy.unenforced with the detail of the status", last)
			}

			configure(t, home, Settings{KeySet: file.Shared.JWKS})
			if status := load(t, gate, tokenSource(token)); status.State != Verified {
				t.Errorf("after a new load: status = %+v, want verified", status)
			}
		})
	}
}

func TestAReadOrWriteWithNoRepoMustBeAllowedInEveryRepo(t *testing.T) {
	keys := newSigner(t)
	home := t.TempDir()
	configure(t, home, Settings{KeySet: keys.keySet(t), MemberID: "mem-ana"})
	bundle := []byte(`{"version": 1, "project_id": "p", "repos": ["api", "web"], "areas": [{"name": "backend", "patterns": ["api:src/**"]}, {"name": "frontend", "patterns": ["web:src/**"]}], "roles": {"backend": {"read": [], "write": ["backend"], "exec": []}}, "members": {"mem-ana": "backend"}}`)
	gate := open(t, home, loadedAt)
	load(t, gate, keys.sign(t, bundle))

	decide := func(queries ...policy.Query) (policy.Query, policy.Decision) {
		verdict, err := gate.Decide("claude-code", queries[0], queries[1:]...)
		if err != nil {
			t.Fatal(err)
		}
		result, _ := verdict.Result()
		return verdict.Query, result.Decision
	}
	write := func(repo, path string) policy.Query {
		return policy.Query{Action: policy.Write, Repo: repo, Path: path}
	}
	if _, decision := decide(write("api", "src/a.ts")); decision != policy.Allow {
		t.Errorf("a write in the repo api: %s, want allow", decision)
	}
	if query, decision := decide(write("", "src/a.ts")); decision != policy.Deny || query.Repo != "web" {
		t.Errorf("a write with no repo: %s in %q, want deny in web", decision, query.Repo)
	}
	if query, decision := decide(write("api", "src/a.ts"), write("web", "src/b.ts"), write("api", "src/c.ts")); decision != policy.Deny || query.Path != "src/b.ts" {
		t.Errorf("a call with three writes: %s for %q, want deny for src/b.ts", decision, query.Path)
	}
	records := auditRecords(t, home)
	last := records[len(records)-1]
	if targets, _ := last.Meta["targets"].([]any); last.Action != "policy.denied" || last.Target != "src/b.ts" || len(targets) != 3 {
		t.Errorf("the audit record of the call is %+v, want policy.denied for src/b.ts with three targets", last)
	}
}

func TestTheEvaluationsOfOneCallShareOneBudget(t *testing.T) {
	keys := newSigner(t)
	home := t.TempDir()
	configure(t, home, Settings{KeySet: keys.keySet(t), MemberID: "mem-ana"})
	bundle := []byte(`{"version": 1, "project_id": "p", "repos": ["r0", "r1", "r2", "r3", "r4", "r5", "r6", "r7", "r8", "r9"], "areas": [{"name": "slow", "patterns": ["*:[]|(?<=(?:.*){2972})]"]}], "roles": {"dev": {"read": [], "write": ["slow"], "exec": []}}, "members": {"mem-ana": "dev"}}`)
	gate := open(t, home, loadedAt)
	load(t, gate, keys.sign(t, bundle))
	path := strings.Repeat("a", 4095) + "]"
	verdict, err := gate.Decide("codex", policy.Query{Action: policy.Write, Repo: "r0", Path: path})
	if result, _ := verdict.Result(); err != nil || result.Decision != policy.Allow {
		t.Fatalf("a write in one repo: %+v, %v, want allow", result, err)
	}
	verdict, err = gate.Decide("codex", policy.Query{Action: policy.Write, Path: path})
	if result, _ := verdict.Result(); err != nil || result.Decision != policy.Deny || !strings.Contains(result.Rule, "budget") {
		t.Fatalf("a write with no repo in ten repos: %+v, %v, want deny, because the call is over its budget", result, err)
	}
}

func TestGateChecksSignedBundleSizesBeforeVerification(t *testing.T) {
	signer := newSigner(t)
	keys := signer.keySet(t)
	previous := signer.sign(t, bundleText("p", 1))
	const prefix = `{"version":2,"project_id":"p","repos":["api"],"areas":[],"roles":{},"members":{},"pad":"`
	const suffix = `"}`
	for _, c := range []struct {
		name, bom, pad    string
		size, compactSize int
		padded, unencoded bool
		wantLimit         error
	}{
		{name: "at the limit", size: policy.MaxBundleBytes},
		{name: "one byte over", size: policy.MaxBundleBytes + 1, wantLimit: policy.ErrBundlePayloadTooLarge},
		{name: "BOM shrinks text below the limit", bom: "\uFEFF", size: policy.MaxBundleBytes + 1, wantLimit: policy.ErrBundlePayloadTooLarge},
		{name: "UTF-8 replacement grows text over the limit", pad: "\x80", size: policy.MaxBundleBytes - 1},
		{name: "padded payload with whitespace at the limit", padded: true, size: policy.MaxBundleBytes},
		{name: "padded payload with whitespace over the limit", padded: true, size: policy.MaxBundleBytes + 1, wantLimit: policy.ErrBundlePayloadTooLarge},
		{name: "unencoded payload at the limit", unencoded: true, size: policy.MaxBundleBytes},
		{name: "unencoded payload over the limit", unencoded: true, size: policy.MaxBundleBytes + 1, wantLimit: policy.ErrBundlePayloadTooLarge},
		{name: "compact token at the limit", size: 128, compactSize: policy.MaxCompactBundleBytes},
		{name: "compact token over the limit", size: 128, compactSize: policy.MaxCompactBundleBytes + 1, wantLimit: policy.ErrCompactBundleTooLarge},
	} {
		start := c.bom + prefix + c.pad
		payload := start + strings.Repeat("x", c.size-len(start)-len(suffix)) + suffix
		var token tokenSource
		switch {
		case c.unencoded:
			header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ES256","typ":"mp-policy+jws","kid":"test","crit":["b64"],"b64":false}`))
			token = signer.signInput(t, header+"."+payload)
		case c.padded:
			header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ES256","typ":"mp-policy+jws","kid":"test"}`))
			segment := base64.URLEncoding.EncodeToString([]byte(payload))
			token = signer.signInput(t, header+". \t"+segment[:len(segment)-1]+"\f"+segment[len(segment)-1:]+"\r\n")
		default:
			token = signer.sign(t, []byte(payload))
		}
		if c.compactSize != 0 {
			token += tokenSource(strings.Repeat(" ", c.compactSize-len(token)))
		}
		message := fmt.Appendf(nil, `{"version":2,"bundle":{},"signature":%q}`, token)
		for _, path := range []string{"load", "sync", "reopen"} {
			t.Run(c.name+"/"+path, func(t *testing.T) {
				home := t.TempDir()
				var signatures, decodes int
				verifier := policy.BundleVerifier{
					BeforeSignature:     func() { signatures++ },
					BeforePayloadDecode: func() { decodes++ },
				}
				checkWork := func() {
					t.Helper()
					if c.wantLimit != nil && (signatures != 0 || decodes != 0) {
						t.Errorf("rejection ran %d signature checks and %d payload decodes, want zero of each", signatures, decodes)
					}
					if c.wantLimit == nil && (signatures == 0 || decodes == 0) {
						t.Errorf("acceptance ran %d signature checks and %d payload decodes, want both", signatures, decodes)
					}
				}
				configure(t, home, Settings{
					KeySet: keys, HubURL: "https://hub.example", ProjectID: "p", MemberID: "mem-ana",
					Token: base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
				})
				if path == "reopen" {
					if err := writeJSON(home, policyFile, storedPolicy{Token: string(token), ConfirmedAt: loadedAt}); err != nil {
						t.Fatal(err)
					}
				}
				gate, err := openGate(home, at(loadedAt), verifier)
				if err != nil {
					t.Fatal(err)
				}
				if path == "reopen" {
					checkWork()
					status := gate.Status()
					if c.wantLimit != nil {
						if status.State != Rejected || !strings.Contains(status.Detail, c.wantLimit.Error()) {
							t.Fatalf("reopen = %+v, want %v", status, c.wantLimit)
						}
					} else if status.State != Verified || status.Version != 2 {
						t.Fatalf("reopen = %+v, want verified version 2", status)
					}
					return
				}

				before := load(t, gate, previous)
				signatures, decodes = 0, 0
				var status Status
				if path == "load" {
					file := filepath.Join(home, "incoming.json")
					if err := os.WriteFile(file, message, 0o600); err != nil {
						t.Fatal(err)
					}
					status, err = gate.Load(t.Context(), FileSource(file))
				} else {
					useHubTransport(t, handlerTransport(func(w http.ResponseWriter, r *http.Request) {
						switch r.URL.Path {
						case "/.well-known/jwks.json":
							if c.wantLimit != nil {
								t.Error("sync requested the advertised keys for an oversized bundle")
							}
							w.Write(keys)
						case "/api/v1/projects/p/policy.bundle":
							w.Write(message)
						default:
							http.NotFound(w, r)
						}
					}))
					status, err = gate.Sync(t.Context())
				}
				checkWork()
				if c.wantLimit != nil {
					if !errors.Is(err, ErrRejected) || !errors.Is(err, c.wantLimit) || !strings.Contains(err.Error(), c.wantLimit.Error()) {
						t.Errorf("incoming bundle error = %v, want %v", err, c.wantLimit)
					}
					if status != before || open(t, home, loadedAt).Status() != before {
						t.Errorf("rejected bundle changed the policy in force: %+v", status)
					}
				} else if err != nil || status.State != Verified || status.Version != 2 {
					t.Errorf("incoming bundle = %+v, %v, want verified version 2", status, err)
				}
			})
		}
	}
}

func TestTheGateOwnsTheMemberAndTheOverridesOfAQuery(t *testing.T) {
	keys := newSigner(t)
	bundle := []byte(`{"version": 1, "project_id": "p", "repos": ["api"], "areas": [{"name": "backend", "patterns": ["api:src/**"]}, {"name": "docs", "patterns": ["api:docs/**"]}], "roles": {"backend": {"read": [], "write": ["backend"], "exec": []}, "writer": {"read": [], "write": ["docs"], "exec": []}}, "members": {"mem-ana": "backend", "mem-wu": "writer"}}`)
	home := t.TempDir()
	configure(t, home, Settings{KeySet: keys.keySet(t), MemberID: "mem-ana"})
	gate := open(t, home, loadedAt)
	load(t, gate, keys.sign(t, bundle))

	session := "ses-1"
	docs := policy.Query{Action: policy.Write, Repo: "api", Path: "docs/a.md", AgentSessionID: &session}
	asAnotherMember := docs
	asAnotherMember.Member = "mem-wu"
	withAnOverride := docs
	withAnOverride.Overrides = []policy.Override{{ID: "o", Area: "docs", Actions: []policy.Action{policy.Write}, Scope: policy.OverrideOnce, AgentSessionID: &session}}
	for name, query := range map[string]policy.Query{
		"a query that names another member": asAnotherMember,
		"a query that brings an override":   withAnOverride,
	} {
		verdict, err := gate.Decide("claude-code", query)
		if err != nil {
			t.Fatal(err)
		}
		if result, _ := verdict.Result(); result.Decision != policy.Deny {
			t.Errorf("%s: %s, want deny", name, result.Decision)
		}
	}
	records := auditRecords(t, home)
	last := records[len(records)-1]
	if last.ActorMemberID == nil || *last.ActorMemberID != "mem-ana" || last.AgentSessionID == nil || *last.AgentSessionID != session || last.Meta["agent"] != "claude-code" {
		t.Errorf("the audit record is %+v, want the configured member, the session of the query, and the agent", last)
	}
}

func TestExecGatedFollowsTheExecGrantsOfTheMemberRole(t *testing.T) {
	keys := newSigner(t)
	bundle := []byte(`{"version": 1, "project_id": "p", "repos": ["api"], "areas": [], "roles": {"runner": {"read": [], "write": [], "exec": ["npm test"]}, "reader": {"read": [], "write": [], "exec": []}}, "members": {"mem-ana": "runner", "mem-wu": "reader"}}`)
	for member, want := range map[string]bool{"mem-ana": true, "mem-wu": false, "mem-unknown": false, "": false} {
		home := t.TempDir()
		configure(t, home, Settings{KeySet: keys.keySet(t), MemberID: member})
		gate := open(t, home, loadedAt)
		if gate.ExecGated() {
			t.Errorf("member %q with no policy: ExecGated is true, want false", member)
		}
		load(t, gate, keys.sign(t, bundle))
		if got := gate.ExecGated(); got != want {
			t.Errorf("member %q after a load: ExecGated is %t, want %t", member, got, want)
		}
		if got := open(t, home, loadedAt).ExecGated(); got != want {
			t.Errorf("member %q in a new process: ExecGated is %t, want %t", member, got, want)
		}
		stale := open(t, home, loadedAt.Add(15*24*time.Hour))
		if got := stale.ExecGated(); stale.Status().State != Stale || got != want {
			t.Errorf("member %q with a %s policy: ExecGated is %t, want %t with a stale policy", member, stale.Status().State, got, want)
		}
	}
}

func TestAnExecRecordHoldsNoCommandText(t *testing.T) {
	keys := newSigner(t)
	home := t.TempDir()
	configure(t, home, Settings{KeySet: keys.keySet(t), MemberID: "mem-ana"})
	gate := open(t, home, loadedAt)
	load(t, gate, keys.sign(t, bundleText("prj-shop", 1)))
	if _, err := gate.Decide("codex", policy.Query{Action: policy.Exec, Command: "deploy --token hunter2"}); err != nil {
		t.Fatal(err)
	}
	if log := string(readFile(t, filepath.Join(home, auditFile))); strings.Contains(log, "hunter2") {
		t.Errorf("the audit log holds the text of the command: %s", log)
	}
}

// Each record that the gate writes must be valid against the published
// schema, and the log must be one chain whatever the gate recorded.
func TestTheAuditLogIsAChainOfRecordsOfTheContract(t *testing.T) {
	var schemaDoc any
	decode(t, readFile(t, filepath.Join(specDir, "audit", "v1", "record.schema.json")), &schemaDoc)
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("record.json", schemaDoc); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("record.json")
	if err != nil {
		t.Fatal(err)
	}

	file := readFixture(t, "verify-signed-bundle.json")
	home := t.TempDir()
	query := policy.Query{Action: policy.Write, Repo: "api", Path: "src/a.ts"}
	decide := func() {
		if _, err := open(t, home, loadedAt).Decide("claude-code", query); err != nil {
			t.Fatal(err)
		}
	}
	decide()
	configure(t, home, Settings{KeySet: file.Shared.JWKS, MemberID: "mem-ana"})
	gate := open(t, home, loadedAt)
	gate.Load(context.Background(), tokenSource(file.token(t, "a payload changed after signing does not verify")))
	load(t, gate, tokenSource(file.token(t, "a bundle signed by a key in the key set verifies")))
	decide()
	query.Path = "docs/a.md"
	decide()

	records := auditRecords(t, home)
	want := []string{"policy.unenforced", "policy.rejected", "policy.loaded", "policy.allowed", "policy.denied"}
	if got := actions(records); !reflect.DeepEqual(got, want) {
		t.Errorf("audit actions = %v, want %v", got, want)
	}
	for i, line := range strings.Split(strings.TrimSuffix(string(readFile(t, filepath.Join(home, auditFile))), "\n"), "\n") {
		var record any
		decode(t, []byte(line), &record)
		if err := schema.Validate(record); err != nil {
			t.Errorf("record %d is not valid against record.schema.json: %v", i, err)
		}
		if records[i].ID != fmt.Sprint(i+1) {
			t.Errorf("record %d has the id %s", i, records[i].ID)
		}
	}
}

func TestConcurrentDecisionsKeepOneChain(t *testing.T) {
	keys := newSigner(t)
	home := t.TempDir()
	configure(t, home, Settings{KeySet: keys.keySet(t), MemberID: "mem-ana"})
	load(t, open(t, home, loadedAt), keys.sign(t, bundleText("prj-shop", 1)))

	const writers, each = 8, 25
	var group sync.WaitGroup
	failures := make(chan error, writers*each)
	for range writers {
		group.Go(func() {
			for range each {
				gate, err := Open(home, at(loadedAt))
				if err == nil {
					_, err = gate.Decide("claude-code", policy.Query{Action: policy.Write, Repo: "api", Path: "src/a.ts"})
				}
				if err != nil {
					failures <- err
				}
			}
		})
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if got := len(auditRecords(t, home)); got != writers*each+1 {
		t.Errorf("the audit log has %d records, want %d", got, writers*each+1)
	}
}

func TestAWriterThatStoppedMidRecordDoesNotBreakTheChain(t *testing.T) {
	home := t.TempDir()
	query := policy.Query{Action: policy.Read, Path: "a"}
	decide := func() error {
		_, err := open(t, home, loadedAt).Decide("claude-code", query)
		return err
	}
	if err := decide(); err != nil {
		t.Fatal(err)
	}
	log, err := os.OpenFile(filepath.Join(home, auditFile), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.WriteString(`{"id":"2","hash":"c0ffee`); err != nil {
		t.Fatal(err)
	}
	log.Close()
	if err := decide(); err != nil {
		t.Fatal(err)
	}
	if got := len(auditRecords(t, home)); got != 2 {
		t.Errorf("the audit log has %d records, want 2", got)
	}

	if err := os.WriteFile(filepath.Join(home, auditFile), []byte("not a record\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := decide(); err == nil {
		t.Error("a log whose last record cannot be read must fail the append, and the append succeeded")
	}
}

func TestOpenFailsOnAConfigurationThatCannotBeRead(t *testing.T) {
	for _, text := range []string{"{", `{"stale_after_days": -1}`, `{"member_id": 7}`} {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, configFile), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(home, time.Now); err == nil {
			t.Errorf("%s: Open succeeded", text)
		}
	}
	if err := Configure(t.TempDir(), Settings{KeySet: []byte(`{"keys": 1}`)}); err == nil {
		t.Error("Configure accepted a key set that is not valid")
	}
}

func TestConfigureChangesNothingWhenTheConfigurationCannotBeRead(t *testing.T) {
	home := t.TempDir()
	first := newSigner(t).keySet(t)
	configure(t, home, Settings{KeySet: first})
	if err := os.WriteFile(filepath.Join(home, configFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Configure(home, Settings{KeySet: newSigner(t).keySet(t), MemberID: "mem-ana"}); err == nil {
		t.Fatal("Configure gave no error for a configuration that is not valid")
	}
	if got := readFile(t, filepath.Join(home, keysFile)); string(got) != string(first) {
		t.Error("a Configure that failed replaced the key set")
	}
}
