package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const policySpec = "../../spec/policy/v1"

var loadedAt = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

type cli struct {
	t   *testing.T
	now time.Time
}

// run gives the exit status, the standard output, and the standard error
// of one command.
func (c cli) run(args ...string) (int, string, string) {
	c.t.Helper()
	var stdout, stderr bytes.Buffer
	status := run(args, &stdout, &stderr, func() time.Time { return c.now })
	return status, stdout.String(), stderr.String()
}

func (c cli) must(want int, args ...string) string {
	c.t.Helper()
	status, stdout, stderr := c.run(args...)
	if status != want {
		c.t.Fatalf("openplexer %s: exit %d, want %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), status, want, stdout, stderr)
	}
	return stdout + stderr
}

func newCLI(t *testing.T) cli {
	t.Helper()
	t.Setenv("OPENPLEXER_HOME", filepath.Join(t.TempDir(), "home"))
	return cli{t: t, now: loadedAt}
}

func readJSON(t *testing.T, path string, into any) {
	t.Helper()
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(text, into); err != nil {
		t.Fatal(err)
	}
}

type verifyFixture struct {
	Shared struct {
		JWKS json.RawMessage `json:"jwks"`
	} `json:"shared"`
	Cases []struct {
		Name  string `json:"name"`
		Input struct {
			Signature string `json:"signature"`
		} `json:"input"`
	} `json:"cases"`
}

// files writes the key set of the verify fixture, and one signed bundle
// message for each named case, and returns their paths.
func fixtureFiles(t *testing.T, cases ...string) (keys string, messages map[string]string) {
	t.Helper()
	var fixture verifyFixture
	readJSON(t, filepath.Join(policySpec, "fixtures", "verify-signed-bundle.json"), &fixture)
	dir := t.TempDir()
	keys = filepath.Join(dir, "keys.json")
	if err := os.WriteFile(keys, fixture.Shared.JWKS, 0o600); err != nil {
		t.Fatal(err)
	}
	messages = map[string]string{}
	for _, c := range fixture.Cases {
		for i, name := range cases {
			if c.Name != name {
				continue
			}
			message, err := json.Marshal(map[string]any{"version": 7, "bundle": map[string]any{}, "signature": c.Input.Signature})
			if err != nil {
				t.Fatal(err)
			}
			messages[name] = filepath.Join(dir, fmt.Sprintf("message-%d.json", i))
			if err := os.WriteFile(messages[name], message, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(messages) != len(cases) {
		t.Fatalf("found %d of the %d cases", len(messages), len(cases))
	}
	return keys, messages
}

// The commands enforce the signed bundle example: each eval-query example
// that needs no override and no mission gets the decision of its
// eval-result example.
func TestCheckEnforcesTheSignedBundleExample(t *testing.T) {
	c := newCLI(t)
	examples := filepath.Join(policySpec, "examples")
	c.must(exitOK, "configure", "--keys", filepath.Join(examples, "jwks", "hub-keys.json"), "--member", "mem-ana")
	if out := c.must(exitOK, "policy", "load", filepath.Join(examples, "signed-bundle", "message.json")); !strings.Contains(out, "policy: verified") || !strings.Contains(out, "role backend") {
		t.Errorf("load printed %q, want a verified policy and the role backend", out)
	}
	exits := map[string]int{"allow": exitOK, "deny": exitDenied}
	for _, name := range []string{"write-allowed", "write-denied", "exec"} {
		var query struct{ Action, Repo, Path, Command string }
		var result struct{ Decision string }
		readJSON(t, filepath.Join(examples, "eval-query", name+".json"), &query)
		readJSON(t, filepath.Join(examples, "eval-result", name+".json"), &result)
		target := query.Path
		if query.Action == "exec" {
			target = query.Command
		}
		out := c.must(exits[result.Decision], "policy", "check", "--repo", query.Repo, query.Action, target)
		if !strings.HasPrefix(out, result.Decision+": ") {
			t.Errorf("%s: check printed %q, want the decision %s and its rule", name, out, result.Decision)
		}
	}
}

func TestLoadRejectsABundleWithABadSignature(t *testing.T) {
	const good, bad = "a bundle signed by a key in the key set verifies", "a payload changed after signing does not verify"
	keys, messages := fixtureFiles(t, good, bad)
	c := newCLI(t)
	c.must(exitOK, "configure", "--keys", keys)

	if out := c.must(exitFailure, "policy", "load", messages[bad]); !strings.Contains(out, "rejected") {
		t.Errorf("load of a bad signature printed %q, want a rejection", out)
	}
	c.must(exitUnenforced, "policy", "status")

	c.must(exitOK, "policy", "load", messages[good])
	c.must(exitFailure, "policy", "load", messages[bad])
	if out := c.must(exitOK, "policy", "status"); !strings.Contains(out, "policy: verified") {
		t.Errorf("status after a rejected load printed %q, want the verified policy", out)
	}
}

func TestStatusReportsAStalePolicyAndCheckStillDecides(t *testing.T) {
	const good = "a bundle signed by a key in the key set verifies"
	keys, messages := fixtureFiles(t, good)
	c := newCLI(t)
	c.must(exitOK, "configure", "--keys", keys, "--member", "mem-ana", "--stale-after-days", "2")
	c.must(exitOK, "policy", "load", messages[good])

	c.now = loadedAt.Add(47 * time.Hour)
	c.must(exitOK, "policy", "status")
	c.now = loadedAt.Add(48 * time.Hour)
	if out := c.must(exitStale, "policy", "status"); !strings.Contains(out, "policy: stale") {
		t.Errorf("status printed %q, want a stale policy", out)
	}
	status, stdout, stderr := c.run("policy", "check", "--repo", "api", "write", "src/a.ts")
	if status == exitUnenforced || status == exitFailure || !strings.Contains(stderr, "stale") || stdout == "" {
		t.Errorf("check under a stale policy: exit %d, stdout %q, stderr %q, want a decision and a notice that the policy is stale", status, stdout, stderr)
	}

	c.must(exitOK, "policy", "load", messages[good])
	c.must(exitOK, "policy", "status")
}

func TestWithNoPolicyTheCommandsSaySo(t *testing.T) {
	c := newCLI(t)
	if out := c.must(exitUnenforced, "policy", "status"); !strings.Contains(out, "policy: absent") || !strings.Contains(out, "member: not configured") {
		t.Errorf("status printed %q, want an absent policy and no member", out)
	}
	if out := c.must(exitUnenforced, "policy", "check", "write", "src/a.ts"); !strings.HasPrefix(out, "no policy: ") {
		t.Errorf("check printed %q, want no policy", out)
	}
	if out := c.must(exitFailure, "policy", "load", filepath.Join(t.TempDir(), "no-such-file.json")); !strings.Contains(out, "not available") {
		t.Errorf("load of a missing file printed %q, want a source that is not available", out)
	}
}

func TestACommandThatIsNotKnownPrintsTheUsage(t *testing.T) {
	c := newCLI(t)
	for _, args := range [][]string{nil, {"--help"}, {"hook", "claude-code", "pre-tool-use"}, {"policy"}, {"policy", "load"}, {"policy", "check", "fly", "x"}, {"configure"}, {"configure", "--stale-after-days", "x"}} {
		if out := c.must(exitUsage, args...); !strings.Contains(out, "usage:") {
			t.Errorf("openplexer %v printed %q, want the usage", args, out)
		}
	}
}
