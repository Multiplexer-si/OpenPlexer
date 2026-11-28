package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multiplexer-si/openplexer/internal/daemon"
	"github.com/multiplexer-si/openplexer/internal/hub"
)

func TestConfigureMaxPolicyAge(t *testing.T) {
	c := newCLI(t)
	configureAgeHome(t, os.Getenv("OPENPLEXER_HOME"), "https://invalid.invalid")
	if out := c.must(exitUnenforced, "policy", "status"); !strings.Contains(out, "maximum policy age: 5m0s") {
		t.Errorf("upgrade default missing from status: %s", out)
	}
	for _, value := range []string{"0s", "59s", "1m1ms", "1h1s", "60.0000000001s", "1m0.0000000001ns", "3600.0000000001s"} {
		if code, _, _ := c.run("configure", "--max-policy-age", value); code != exitFailure {
			t.Errorf("configure accepted %q with exit %d", value, code)
		}
	}
	for _, value := range []string{"1m", "0.5m30s", "59.9999999999s0.0000000001s", "1h"} {
		c.must(exitOK, "configure", "--max-policy-age", value)
	}
	out := c.must(exitUnenforced, "policy", "status")
	for _, field := range []string{"maximum policy age: 1h0m0s", "hub confirmed at: never", "daemon heartbeat:"} {
		if !strings.Contains(out, field) {
			t.Errorf("status missing %q: %s", field, out)
		}
	}
}

func configureAgeHome(t *testing.T, home, origin string) {
	t.Helper()
	keys := readAgeFixture(t, filepath.Join(policySpec, "examples", "jwks", "hub-keys.json"))
	if err := daemon.Configure(home, daemon.Settings{HubURL: origin, ProjectID: "prj-shop",
		MemberID: "mem-ana", Token: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), KeySet: keys}); err != nil {
		t.Fatal(err)
	}
}

func readAgeFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSessionStartInstallsAndWarns(t *testing.T) {
	for _, agent := range []string{"claude-code", "codex"} {
		for _, portable := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/portable=%t", agent, portable), func(t *testing.T) {
				c := newCLI(t)
				home := os.Getenv("OPENPLEXER_HOME")
				t.Setenv("HOME", t.TempDir())
				project := t.TempDir()
				c.must(exitOK, "configure", "--member", "mem-ana")
				args := []string{"install", agent, "--project", project}
				hookArgs := []string{"hook", agent, "session-start"}
				if portable {
					args = append(args, "--portable")
					hookArgs = append(hookArgs, "--project-home")
				}
				c.must(exitOK, args...)
				c.must(exitOK, args...)
				agentFile := ".codex/hooks.json"
				if agent == "claude-code" {
					agentFile = ".claude/settings.json"
				}
				path := filepath.Join(project, agentFile)
				text := string(readAgeFixture(t, path))
				if strings.Count(text, `"SessionStart"`) != 1 || strings.Count(text, "session-start") != 1 {
					t.Errorf("expected exactly one startup handler, got %s", text)
				}
				c.stdin = fmt.Sprintf(`{"hook_event_name":"SessionStart","cwd":%q,"source":"startup","session_id":"s"}`, project)
				if out := c.must(exitOK, hookArgs...); out != "" {
					t.Errorf("local home startup output: %s", out)
				}
				fixture := newAgeHub(t, home, "http://127.0.0.1:1")
				message := fixture.publish(1, true)
				file := filepath.Join(t.TempDir(), "bundle.json")
				if err := os.WriteFile(file, message, 0o600); err != nil {
					t.Fatal(err)
				}
				c.must(exitOK, "policy", "load", file)
				if out := c.must(exitOK, hookArgs...); !strings.Contains(out, `"systemMessage"`) || !strings.Contains(out, "openplexer policy sync") {
					t.Errorf("expected actionable warning, got %s", out)
				}
				c.must(exitStale, "policy", "check-session")
				c.must(exitOK, "uninstall", agent, "--project", project)
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("uninstall left the hook file: %v", err)
				}
			})
		}
	}
}

type ageHubFixture struct {
	t     *testing.T
	store *hub.Store
	lead  string
}

func newAgeHub(t *testing.T, home, origin string) ageHubFixture {
	t.Helper()
	store := hub.Open(t.TempDir())
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(store.Init())
	must(store.AddProject("prj-shop", "api"))
	lead, err := store.AddMember("mem-lee", hub.Lead)
	must(err)
	member, err := store.AddMember("mem-ana", hub.Member)
	must(err)
	keys, err := store.PolicyKeys()
	must(err)
	jwks, err := json.Marshal(keys)
	must(err)
	must(daemon.Configure(home, daemon.Settings{HubURL: origin, ProjectID: "prj-shop", MemberID: "mem-ana", Token: member, KeySet: jwks}))
	return ageHubFixture{t: t, store: store, lead: lead}
}

func (h ageHubFixture) publish(version int, allow bool) []byte {
	h.t.Helper()
	grant := ""
	if allow {
		grant = `"backend"`
	}
	bundle := fmt.Sprintf(`{"version":%d,"project_id":"prj-shop","repos":["api"],"areas":[{"name":"backend","patterns":["api:src/**"]}],"roles":{"backend":{"read":[],"write":[%s],"exec":[]}},"members":{"mem-ana":"backend"}}`, version, grant)
	r := httptest.NewRequest(http.MethodPut, "/api/v1/projects/prj-shop/policy", strings.NewReader(bundle))
	r.Header.Set("Authorization", "Bearer "+h.lead)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.store.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		h.t.Fatalf("lead publish: HTTP %d", w.Code)
	}
	return w.Body.Bytes()
}

type ageTransport func(*http.Request) (*http.Response, error)

func (f ageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testAgeTransport(t *testing.T, serve ageTransport) {
	t.Helper()
	old := http.DefaultTransport
	http.DefaultTransport = serve
	t.Cleanup(func() { http.DefaultTransport = old })
}

func ageResponse(handler http.Handler, r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w.Result(), nil
}

func ageProject(t *testing.T) string {
	t.Helper()
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	return project
}

func ageWrite(project string) string {
	return fmt.Sprintf(`{"hook_event_name":"PreToolUse","tool_name":"Write","cwd":%q,"session_id":"s1","tool_input":{"file_path":"src/app.ts"}}`, project)
}

func TestHookRequiresPolicyForZeroQueryShell(t *testing.T) {
	for _, agent := range []string{"claude-code", "codex"} {
		for _, portable := range []bool{false, true} {
			for _, mode := range []string{"local", "hub-offline", "hub-online"} {
				t.Run(fmt.Sprintf("%s/portable=%t/%s", agent, portable, mode), func(t *testing.T) {
					c := newCLI(t)
					c.bin = ""
					userHome := t.TempDir()
					t.Setenv("HOME", userHome)
					project := filepath.Join(userHome, "project")
					if err := os.MkdirAll(filepath.Join(project, ".git"), 0o700); err != nil {
						t.Fatal(err)
					}
					home := os.Getenv("OPENPLEXER_HOME")
					fetches := 0
					var message string
					if mode == "local" {
						keys, signed := signedPolicy(t, testBundle)
						message = signed
						c.must(exitOK, "configure", "--keys", keys, "--member", "mem-dev")
					} else {
						fixture := newAgeHub(t, home, "https://age.invalid")
						fixture.publish(1, true)
						testAgeTransport(t, func(r *http.Request) (*http.Response, error) {
							if strings.HasSuffix(r.URL.Path, "policy.bundle") {
								fetches++
								if mode == "hub-offline" {
									w := httptest.NewRecorder()
									w.WriteHeader(http.StatusServiceUnavailable)
									return w.Result(), nil
								}
							}
							return ageResponse(fixture.store.Handler(), r)
						})
					}
					c.must(exitOK, "configure", "--project-root", project)
					args := []string{"hook", agent, "pre-tool"}
					if portable {
						args = append(args, "--project-home")
					}
					if out := c.must(exitUnenforced, "policy", "status"); !strings.Contains(out, "policy: absent") {
						t.Fatal("the test must start without a stored policy")
					}
					c.stdin = callJSON("Bash", project, map[string]string{"command": "tee"})
					// The hook adds elapsed time to its captured clock. Advance
					// past that window while staying within refresh backoff.
					afterHook := c.now.Add(hookBudget)
					switch mode {
					case "local":
						if out := c.must(exitOK, args...); !strings.Contains(out, `"permissionDecision":"deny"`) || !strings.Contains(out, "no policy loaded") {
							t.Errorf("absent local policy returned %q, want a denial naming the absent policy", out)
						}
						c.must(exitOK, "policy", "load", message)
					case "hub-offline":
						if out := c.must(exitOK, args...); !strings.Contains(out, `"permissionDecision":"deny"`) || !strings.Contains(out, daemon.StalePolicyReason) {
							t.Errorf("absent hub policy returned %q, want a freshness denial", out)
						}
						audit, err := os.ReadFile(filepath.Join(home, "audit.jsonl"))
						if err != nil || !bytes.Contains(audit, []byte(`"action":"policy.denied"`)) || !bytes.Contains(audit, []byte(daemon.StalePolicyReason)) {
							t.Errorf("absent hub policy must audit the freshness denial, read error %v", err)
						}
						c.now = afterHook
						c.stdin = writeCall(agent, project, "src/app.ts")
						if out := c.must(exitOK, args...); !strings.Contains(out, `"permissionDecision":"deny"`) || !strings.Contains(out, daemon.StalePolicyReason) {
							t.Errorf("write during initial refresh backoff returned %q, want a freshness denial", out)
						}
						if fetches != 1 {
							t.Errorf("absent hub policy fetched %d bundles, want one attempt", fetches)
						}
						return
					case "hub-online":
						if out := c.must(exitOK, args...); out != "" {
							t.Errorf("shell after initial sync returned %q, want an empty allow", out)
						}
						if fetches != 1 {
							t.Errorf("absent hub policy fetched %d bundles, want one attempt", fetches)
						}
						c.now = afterHook
						c.must(exitOK, "policy", "status")
					}
					if out := c.must(exitOK, args...); out != "" {
						t.Errorf("shell with verified policy returned %q, want an empty allow", out)
					}
					if mode == "hub-online" && fetches != 1 {
						t.Errorf("fresh hub policy fetched again, total attempts %d", fetches)
					}
				})
			}
		}
	}
}

func TestHookBlocksOnRefreshReopenError(t *testing.T) {
	for _, agent := range []string{"claude-code", "codex"} {
		for _, portable := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/portable=%t", agent, portable), func(t *testing.T) {
				c := newCLI(t)
				c.bin = ""
				userHome := t.TempDir()
				t.Setenv("HOME", userHome)
				t.Setenv("CLAUDE_CONFIG_DIR", "")
				t.Setenv("CODEX_HOME", "")
				home, project := os.Getenv("OPENPLEXER_HOME"), filepath.Join(userHome, "project")
				if err := os.MkdirAll(filepath.Join(project, ".git"), 0o700); err != nil {
					t.Fatal(err)
				}
				fixture := newAgeHub(t, home, "https://age.invalid")
				fixture.publish(1, true)
				testAgeTransport(t, func(r *http.Request) (*http.Response, error) {
					return ageResponse(fixture.store.Handler(), r)
				})
				args := []string{"hook", agent, "pre-tool"}
				if portable {
					c.must(exitOK, "configure", "--project-root", project)
					args = append(args, "--project-home")
				}
				c.must(exitOK, "policy", "sync")
				shell := callJSON("Bash", project, map[string]string{"command": "tee"})
				write := ageWrite(project)
				if agent == "codex" {
					write = callJSON("apply_patch", project, map[string]string{"command": "*** Begin Patch\n*** Add File: src/app.ts\n+new\n*** End Patch\n"})
				}
				for _, call := range []struct{ name, input string }{{"shell", shell}, {"write", write}} {
					t.Run(call.name, func(t *testing.T) {
						c := c
						c.t, c.stdin = t, call.input
						if out := c.must(exitOK, args...); out != "" {
							t.Fatalf("fresh call returned %q, want allow", out)
						}
						path := filepath.Join(home, "config.json")
						var stdout, stderr strings.Builder
						injected := false
						code := run(args, strings.NewReader(call.input), &stdout, &stderr, func() time.Time {
							// Open has read the config when it first asks for the clock.
							// Make the next read fail under the refresh lock.
							if !injected {
								if err := os.Rename(path, path+".saved"); err != nil {
									t.Fatal(err)
								}
								if err := os.Mkdir(path, 0o700); err != nil {
									t.Fatal(err)
								}
								t.Cleanup(func() {
									if err := os.Remove(path); err != nil {
										t.Error(err)
									}
									if err := os.Rename(path+".saved", path); err != nil {
										t.Error(err)
									}
								})
								injected = true
							}
							return c.now.Add(5 * time.Minute)
						})
						out := stdout.String()
						if !injected || code != exitOK || !strings.Contains(out, `"permissionDecision":"deny"`) ||
							!strings.Contains(out, "cannot read its configuration") || !strings.Contains(out, "config.json") {
							t.Errorf("reopen error: exit %d, stdout %q, stderr %q, want a denial identifying config.json", code, out, stderr.String())
						}
					})
				}
			})
		}
	}
}

type hookRefreshCase struct {
	name, file, reason string
	unreadable         bool
	duringFetch        bool
	rejected           bool
	blockBackoffWrite  bool
	fetches            int
}

func TestHookBindsRequiredRefresh(t *testing.T) {
	for _, scenario := range []hookRefreshCase{
		{name: "failed-fetch", reason: daemon.StalePolicyReason, fetches: 1},
		{name: "rejected-bundle", reason: daemon.StalePolicyReason, rejected: true, fetches: 1},
		{name: "backoff-write-error", reason: "policy-refresh-failure.json", blockBackoffWrite: true, fetches: 1},
		{name: "missing-config", file: "config.json", reason: "config.json"},
		{name: "missing-policy", file: "policy.json", reason: "policy.json"},
		{name: "unreadable-config", file: "config.json", reason: "config.json", unreadable: true},
		{name: "unreadable-policy", file: "policy.json", reason: "policy.json", unreadable: true},
		{name: "policy-lost-during-fetch", file: "policy.json", reason: "policy.json", duringFetch: true, fetches: 1},
	} {
		for _, agent := range []string{"claude-code", "codex"} {
			for _, portable := range []bool{false, true} {
				for _, tool := range []string{"zero-command", "matched-shell", "native-write"} {
					name := fmt.Sprintf("%s/%s/portable=%t/%s", scenario.name, agent, portable, tool)
					t.Run(name, func(t *testing.T) {
						testHookRequiredRefresh(t, agent, portable, tool, scenario)
					})
				}
			}
		}
	}
}

func testHookRequiredRefresh(t *testing.T, agent string, portable bool, tool string, scenario hookRefreshCase) {
	t.Helper()
	c := newCLI(t)
	c.bin = ""
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	home, project := os.Getenv("OPENPLEXER_HOME"), ageProject(t)
	removeFile := func() {
		path := filepath.Join(home, scenario.file)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if scenario.unreadable {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	fixture := newAgeHub(t, home, "https://age.invalid")
	oldBundle := fixture.publish(1, true)
	fixture.publish(2, true)
	checking := false
	fetched := 0
	testAgeTransport(t, func(r *http.Request) (*http.Response, error) {
		if checking && strings.HasSuffix(r.URL.Path, "policy.bundle") {
			fetched++
			if scenario.duringFetch {
				removeFile()
				return ageResponse(fixture.store.Handler(), r)
			}
			if scenario.blockBackoffWrite {
				if err := os.Mkdir(filepath.Join(home, "policy-refresh-failure.json"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			w := httptest.NewRecorder()
			if scenario.rejected {
				w.Write(oldBundle)
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			return w.Result(), nil
		}
		return ageResponse(fixture.store.Handler(), r)
	})
	args := []string{"hook", agent, "pre-tool"}
	if portable {
		c.must(exitOK, "configure", "--project-root", project)
		args = append(args, "--project-home")
	}
	c.must(exitOK, "policy", "sync")
	switch tool {
	case "zero-command":
		c.stdin = callJSON("Bash", project, map[string]string{"command": "tee"})
	case "matched-shell":
		c.stdin = callJSON("Bash", project, map[string]string{"command": "echo ok"})
	case "native-write":
		c.stdin = ageWrite(project)
		if agent == "codex" {
			c.stdin = callJSON("apply_patch", project, map[string]string{"command": "*** Begin Patch\n*** Add File: src/app.ts\n+new\n*** End Patch\n"})
		}
	}
	if code, out, diagnostic := c.run(args...); code != exitOK || out != "" || diagnostic != "" {
		t.Fatalf("fresh control returned exit %d, stdout %q, stderr %q", code, out, diagnostic)
	}
	policyPath := filepath.Join(home, "policy.json")
	before := readAgeFixture(t, policyPath)
	checking = true
	reads := 0
	var stdout, stderr strings.Builder
	code := run(args, strings.NewReader(c.stdin), &stdout, &stderr, func() time.Time {
		reads++
		if scenario.file != "" {
			if reads == 1 && !scenario.duringFetch {
				removeFile()
			}
			return c.now.Add(5*time.Minute + time.Duration(reads-1)*time.Millisecond)
		}
		// The old confirmation is a millisecond in the future until the
		// failed attempt has written its backoff marker.
		if _, err := os.Stat(filepath.Join(home, "policy-refresh-failure.json")); os.IsNotExist(err) {
			return c.now.Add(-time.Millisecond)
		}
		return c.now
	})
	if out := stdout.String(); code != exitOK || !strings.Contains(out, `"permissionDecision":"deny"`) || !strings.Contains(out, scenario.reason) {
		t.Errorf("required refresh returned exit %d, stdout %q, stderr %q; want denial mentioning %q", code, out, stderr.String(), scenario.reason)
	}
	if reads != 1 || fetched != scenario.fetches {
		t.Errorf("hook read the clock %d times and fetched %d bundles; want one clock read and %d fetches", reads, fetched, scenario.fetches)
	}
	if scenario.file == "" {
		if !bytes.Equal(before, readAgeFixture(t, policyPath)) {
			t.Error("the unsuccessful refresh changed the cached policy")
		}
		if scenario.rejected {
			audit := readAgeFixture(t, filepath.Join(home, "audit.jsonl"))
			if !bytes.Contains(audit, []byte(`"action":"policy.rejected"`)) {
				t.Error("the HTTP success did not reach bundle admission")
			}
		}
	}
}

func TestRejectedHubHookRefreshesAndDenies(t *testing.T) {
	for _, binding := range []string{"project", "pin"} {
		for _, portable := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/portable=%t", binding, portable), func(t *testing.T) {
				c := newCLI(t)
				c.bin = ""
				t.Setenv("HOME", t.TempDir())
				home, project := os.Getenv("OPENPLEXER_HOME"), ageProject(t)
				fixture := newAgeHub(t, home, "https://age.invalid")
				fixture.publish(1, true)
				fetches := 0
				testAgeTransport(t, func(r *http.Request) (*http.Response, error) {
					if strings.HasSuffix(r.URL.Path, "policy.bundle") {
						fetches++
					}
					return ageResponse(fixture.store.Handler(), r)
				})
				args := []string{"hook", "claude-code", "pre-tool"}
				if portable {
					c.must(exitOK, "configure", "--project-root", project)
					args = append(args, "--project-home")
				}
				c.must(exitOK, "policy", "sync")
				shell := fmt.Sprintf(`{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":%q,"tool_input":{"command":"tee"}}`, project)
				c.stdin = shell
				if out := c.must(exitOK, args...); out != "" {
					t.Fatalf("fresh shell got %q, want allow", out)
				}

				if binding == "project" {
					// Configure deliberately refuses to switch projects in an existing hub home.
					path := filepath.Join(home, "config.json")
					var config map[string]json.RawMessage
					readJSON(t, path, &config)
					config["project_id"] = json.RawMessage(`"prj-other"`)
					data, err := json.Marshal(config)
					if err != nil {
						t.Fatal("could not encode the test config")
					}
					if err := os.WriteFile(path, data, 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					pin, _ := signedPolicy(t, testBundle)
					c.must(exitOK, "configure", "--keys", pin)
				}
				c.must(exitUnenforced, "policy", "status")
				for _, input := range []string{shell, ageWrite(project)} {
					c.stdin = input
					out := c.must(exitOK, args...)
					if !strings.Contains(out, `"permissionDecision":"deny"`) || !strings.Contains(out, "cannot verify its policy") || fetches != 2 {
						t.Errorf("rejected cache got %q after %d bundle fetches, want denial and one refresh", out, fetches)
					}
					c.now = c.now.Add(daemon.RefreshBudget)
				}
			})
		}
	}
}

func TestStaleHookRefreshOrDeny(t *testing.T) {
	for _, reachable := range []bool{true, false} {
		t.Run(fmt.Sprint(reachable), func(t *testing.T) {
			c := newCLI(t)
			c.bin = ""
			t.Setenv("HOME", t.TempDir())
			home := os.Getenv("OPENPLEXER_HOME")
			project := ageProject(t)
			fixture := newAgeHub(t, home, "https://age.invalid")
			fixture.publish(1, true)
			online := true
			fetches := 0
			testAgeTransport(t, func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "policy.bundle") {
					fetches++
				}
				if !online {
					return nil, fmt.Errorf("fake hub offline")
				}
				return ageResponse(fixture.store.Handler(), r)
			})
			c.must(exitOK, "configure", "--max-policy-age", "1m")
			c.must(exitOK, "policy", "sync")
			c.stdin = ageWrite(project)
			c.must(exitOK, "hook", "claude-code", "pre-tool")
			fixture.publish(2, false)
			c.now = c.now.Add(time.Minute)
			online = reachable
			c.must(exitStale, "policy", "status")
			if fetches != 1 {
				t.Fatalf("status made a request")
			}
			for _, tc := range []struct{ action, target string }{{"write", "src/app.ts"}, {"read", "src/app.ts"}, {"git.push", "api"}, {"exec", "echo ok"}} {
				out := c.must(exitDenied, "policy", "check", tc.action, tc.target)
				if !strings.Contains(out, "openplexer policy sync") {
					t.Errorf("check %s missed freshness: %s", tc.action, out)
				}
			}
			if fetches != 1 {
				t.Fatalf("policy check made a request")
			}
			out := c.must(exitOK, "hook", "claude-code", "pre-tool")
			if !strings.Contains(out, `"permissionDecision":"deny"`) ||
				strings.Contains(out, "openplexer policy sync") == reachable || fetches != 2 {
				t.Errorf("hook returned %q after %d bundle fetches", out, fetches)
			}
			c.now = c.now.Add(daemon.RefreshBudget)
			if reachable {
				if out := c.must(exitOK, "policy", "status"); !strings.Contains(out, "version 2 ") {
					t.Errorf("refreshed policy missing: %s", out)
				}
			} else {
				c.stdin = fmt.Sprintf(`{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":%q,"session_id":"s1","tool_input":{"command":"echo ok"}}`, project)
				if out := c.must(exitOK, "hook", "codex", "pre-tool"); !strings.Contains(out, "openplexer policy sync") || fetches != 2 {
					t.Errorf("shell/backoff: %q, %d fetches", out, fetches)
				}
				if entries, err := os.ReadDir(filepath.Join(home, "room-outbox")); err != nil || len(entries) != 2 {
					t.Errorf("expected two attributable denials, got %d, %v", len(entries), err)
				}
				c.stdin = fmt.Sprintf(`{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":%q,"tool_input":{"command":"echo ok"}}`, t.TempDir())
				if out := c.must(exitOK, "hook", "codex", "pre-tool"); !strings.Contains(out, "openplexer policy sync") || fetches != 2 {
					t.Errorf("matched shell without a project root: %q, %d fetches", out, fetches)
				}
				online = true
				c.must(exitOK, "policy", "sync")
				if fetches != 3 {
					t.Errorf("manual sync did not bypass backoff")
				}
			}
		})
	}
}

func TestStaleHookConcurrentRefresh(t *testing.T) {
	c := newCLI(t)
	c.bin = ""
	t.Setenv("HOME", t.TempDir())
	home, project := os.Getenv("OPENPLEXER_HOME"), ageProject(t)
	fixture := newAgeHub(t, home, "https://age.invalid")
	fixture.publish(1, true)
	var fetches atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	testAgeTransport(t, func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "policy.bundle") && fetches.Add(1) == 2 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return ageResponse(fixture.store.Handler(), r)
	})
	c.must(exitOK, "policy", "sync")
	c.now = c.now.Add(5 * time.Minute)
	c.stdin = ageWrite(project)
	started := time.Now()
	clock := func() time.Time { return c.now.Add(time.Since(started)) }
	var calls sync.WaitGroup
	runHook := func() {
		var out, diagnostic strings.Builder
		exit := run([]string{"hook", "claude-code", "pre-tool"}, strings.NewReader(c.stdin), &out, &diagnostic, clock)
		if exit != exitOK || out.Len() != 0 || diagnostic.Len() != 0 {
			t.Errorf("hook: %d %q %q", exit, out.String(), diagnostic.String())
		}
	}
	calls.Go(runHook)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Error("stale hook never attempted a refresh")
	}
	calls.Go(runHook)
	close(release)
	calls.Wait()
	if fetches.Load() != 2 {
		t.Errorf("got %d requests; want initial sync plus one shared refresh", fetches.Load())
	}
}

func TestStalePortableHookSharedBudget(t *testing.T) {
	c := newCLI(t)
	c.bin = ""
	t.Setenv("HOME", t.TempDir())
	outer := ageProject(t)
	inner := filepath.Join(outer, "inner")
	if err := os.Mkdir(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	home := os.Getenv("OPENPLEXER_HOME")
	second := filepath.Join(t.TempDir(), "home")
	a := newAgeHub(t, home, "https://first.invalid")
	b := newAgeHub(t, second, "https://second.invalid")
	a.publish(1, true)
	b.publish(1, true)
	slow := false
	var attempts []string
	testAgeTransport(t, func(r *http.Request) (*http.Response, error) {
		if slow && strings.HasSuffix(r.URL.Path, "policy.bundle") {
			attempts = append(attempts, r.URL.Host)
			if r.URL.Host == "second.invalid" {
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
			select {
			case <-time.After(1300 * time.Millisecond):
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		h := a.store
		if r.URL.Host == "second.invalid" {
			h = b.store
		}
		return ageResponse(h.Handler(), r)
	})
	c.must(exitOK, "--home", home, "configure", "--project-root", outer)
	c.must(exitOK, "--home", second, "configure", "--project-root", inner)
	c.must(exitOK, "--home", home, "policy", "sync")
	c.must(exitOK, "--home", second, "policy", "sync")
	c.now = c.now.Add(5 * time.Minute)
	slow = true
	c.stdin = fmt.Sprintf(`{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":%q,"tool_input":{"command":"touch src/a inner/src/b"}}`, outer)
	start := time.Now()
	out := c.must(exitOK, "hook", "codex", "pre-tool", "--project-home")
	elapsed := time.Since(start)
	if !strings.Contains(out, "openplexer policy sync") || strings.Join(attempts, ",") != "first.invalid,second.invalid" || elapsed < 1800*time.Millisecond || elapsed > 3*time.Second {
		t.Errorf("portable hook took %s, attempts %v, output %q", elapsed, attempts, out)
	}
}

func TestStaleHookSubprocess(t *testing.T) {
	binary := prebuiltCLI
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "openplexer")
		build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build openplexer: %v\n%s", err, output)
		}
	}
	for _, reachable := range []bool{true, false} {
		t.Run(fmt.Sprint(reachable), func(t *testing.T) {
			c := newCLI(t)
			c.bin = binary
			t.Setenv("HOME", t.TempDir())
			home := os.Getenv("OPENPLEXER_HOME")
			origin := "http://127.0.0.1:1"
			var listener net.Listener
			if reachable {
				var err error
				listener, err = net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { listener.Close() })
				origin = "http://" + listener.Addr().String()
			}
			fixture := newAgeHub(t, home, origin)
			if reachable {
				server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: fixture.store.Handler()}}
				server.Start()
				t.Cleanup(server.Close)
			} else {
				testAgeTransport(t, func(r *http.Request) (*http.Response, error) {
					return ageResponse(fixture.store.Handler(), r)
				})
			}
			fixture.publish(1, true)
			initial := c
			initial.bin, initial.now = "", time.Now().Add(-2*time.Minute)
			c.must(exitOK, "configure", "--max-policy-age", "1m")
			initial.must(exitOK, "policy", "sync")
			fixture.publish(2, false)
			c.stdin = ageWrite(ageProject(t))
			out := c.must(exitOK, "hook", "claude-code", "pre-tool")
			if !strings.Contains(out, `"permissionDecision":"deny"`) || strings.Contains(out, "openplexer policy sync") == reachable {
				t.Errorf("real hook returned %q", out)
			}
		})
	}
}

func TestCheckSessionRefreshesHubAndPreservesLocalAdvisory(t *testing.T) {
	c := newCLI(t)
	c.bin = ""
	home := os.Getenv("OPENPLEXER_HOME")
	fixture := newAgeHub(t, home, "https://age.invalid")
	fixture.publish(1, true)
	fetches := 0
	testAgeTransport(t, func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "policy.bundle") {
			fetches++
		}
		return ageResponse(fixture.store.Handler(), r)
	})
	c.must(exitOK, "policy", "sync")
	c.now = c.now.Add(5 * time.Minute)
	out := c.must(exitOK, "policy", "check-session")
	var saved struct {
		HubConfirmedAt time.Time `json:"hub_confirmed_at"`
	}
	readJSON(t, filepath.Join(home, "policy.json"), &saved)
	if saved.HubConfirmedAt.Before(c.now) || saved.HubConfirmedAt.Sub(c.now) > daemon.RefreshBudget ||
		!strings.Contains(out, saved.HubConfirmedAt.Format(time.RFC3339Nano)) || fetches != 2 {
		t.Errorf("startup check returned %q after %d fetches", out, fetches)
	}
	c.now = c.now.Add(5 * time.Minute)
	project := ageProject(t)
	c.stdin = fmt.Sprintf(`{"hook_event_name":"PreToolUse","cwd":%q,"tool_name":"Read","tool_input":{"file_path":"src/app.ts"}}`, project)
	if out := c.must(exitOK, "hook", "claude-code", "pre-tool"); out != "" || fetches != 2 {
		t.Errorf("unmatched tool returned %q after %d fetches", out, fetches)
	}

	local := loadedCLI(t)
	local.bin = ""
	local.must(exitOK, "configure", "--max-policy-age", "1m")
	local.now = loadedAt.Add(14 * 24 * time.Hour)
	local.must(exitStale, "policy", "check-session")
	local.must(exitOK, "policy", "check", "write", "src/app.ts")
	if fetches != 2 {
		t.Errorf("local home made a hub request")
	}
}
