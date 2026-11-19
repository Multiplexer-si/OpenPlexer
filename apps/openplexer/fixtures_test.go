package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestPolicyCheckFixtures(t *testing.T) {
	if prebuiltCLI == "" {
		t.Skip("set OPENPLEXER_BIN_DIR to check the policy fixtures through a binary")
	}
	paths, err := filepath.Glob(filepath.Join(policySpec, "fixtures", "evaluate-*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("find evaluate fixtures: %v", err)
	}
	for _, path := range paths {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			var file struct {
				Shared struct{ Bundle json.RawMessage }
				Cases  []struct {
					Name  string
					Input struct {
						Action, Command, Member, Path, Repo string
						Session                             string          `json:"agent_session_id"`
						Mission                             json.RawMessage `json:"mission_id"`
						Overrides                           json.RawMessage
						Parent                              json.RawMessage
					}
					Expected struct {
						Decision string
						Throws   bool
					}
				}
			}
			readJSON(t, path, &file)
			if name == "evaluate-temporary-grants.json" {
				t.Skip("policy check uses the current clock")
			}
			keys, message := signedPolicy(t, file.Shared.Bundle)
			for _, tc := range file.Cases {
				t.Run(tc.Name, func(t *testing.T) {
					q := tc.Input
					if q.Mission != nil || q.Overrides != nil || q.Parent != nil {
						t.Skip("policy check has no mission, override, or parent arguments")
					}
					c := newCLI(t)
					c.must(exitOK, "configure", "--keys", keys, "--member", q.Member)
					c.must(exitOK, "policy", "load", message)
					target := q.Path
					switch q.Action {
					case "exec":
						target = q.Command
					case "git.push":
						target = q.Repo
					}
					want, exit := tc.Expected.Decision, exitOK
					if want == "deny" || tc.Expected.Throws {
						want, exit = "deny", exitDenied
					}
					out := c.must(exit, "policy", "check", "--repo", q.Repo, "--session", q.Session, "--", q.Action, target)
					if !strings.HasPrefix(out, want+": ") {
						t.Errorf("policy check printed %q, want %s and its rule", out, want)
					}
				})
			}
		})
	}
}

func TestPolicyLoadFixtures(t *testing.T) {
	if prebuiltCLI == "" {
		t.Skip("set OPENPLEXER_BIN_DIR to check the policy fixtures through a binary")
	}
	for _, name := range []string{"verify-signed-bundle.json", "verify-signed-bundle-key-selection.json"} {
		t.Run(name, func(t *testing.T) {
			var file struct {
				Shared struct{ JWKS json.RawMessage }
				Cases  []struct {
					Name     string
					Input    struct{ Signature string }
					Expected struct{ OK bool }
				}
			}
			readJSON(t, filepath.Join(policySpec, "fixtures", name), &file)
			dir := t.TempDir()
			keys := filepath.Join(dir, "keys.json")
			writeFile(t, keys, string(file.Shared.JWKS))
			for _, tc := range file.Cases {
				t.Run(tc.Name, func(t *testing.T) {
					c := newCLI(t)
					c.must(exitOK, "configure", "--keys", keys)
					message := filepath.Join(t.TempDir(), "message.json")
					data, err := json.Marshal(map[string]any{"version": 1, "bundle": nil, "signature": tc.Input.Signature})
					if err != nil {
						t.Fatal(err)
					}
					writeFile(t, message, string(data))
					wantExit, want := exitFailure, "the bundle is rejected"
					if tc.Expected.OK {
						wantExit, want = exitOK, "policy: verified"
					}
					if out := c.must(wantExit, "policy", "load", message); !strings.Contains(out, want) {
						t.Errorf("policy load printed %q, want %q", out, want)
					}
				})
			}
		})
	}
}
