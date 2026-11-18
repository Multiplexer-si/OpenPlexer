package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain moves every home that a command reads or writes into a temporary
// directory, so that no test can reach the developer's own configuration.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "openplexer-test")
	if err != nil {
		panic(err)
	}
	for _, name := range []string{"HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "OPENPLEXER_HOME"} {
		os.Setenv(name, filepath.Join(dir, strings.ToLower(name)))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

var testBundle = map[string]any{
	"project_id": "prj-test",
	"version":    1,
	"repos":      []string{"app"},
	"areas":      []any{map[string]any{"name": "source", "patterns": []string{"app:src/**"}}},
	"roles": map[string]any{
		"dev": map[string]any{"read": []string{}, "write": []string{"source"}, "exec": []string{"go test"}, "push": []string{}},
	},
	"members": map[string]string{"mem-dev": "dev"},
}

func signedPolicy(t *testing.T, bundle any) (keys, message string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	payload, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	signingInput := b64([]byte(`{"alg":"ES256","typ":"mp-policy+jws"}`)) + "." + b64(payload)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	signature := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	point, err := key.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	jwks, _ := json.Marshal(map[string]any{"keys": []any{map[string]string{"kty": "EC", "crv": "P-256", "x": b64(point[1:33]), "y": b64(point[33:])}}})
	msg, _ := json.Marshal(map[string]any{"version": 1, "bundle": bundle, "signature": signingInput + "." + b64(signature)})
	dir := t.TempDir()
	keys, message = filepath.Join(dir, "keys.json"), filepath.Join(dir, "message.json")
	for path, text := range map[string][]byte{keys: jwks, message: msg} {
		if err := os.WriteFile(path, text, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return keys, message
}

func project(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func callJSON(tool, cwd string, input map[string]string) string {
	text, _ := json.Marshal(map[string]any{
		"session_id": "ses-1", "hook_event_name": "PreToolUse", "tool_name": tool, "cwd": cwd, "tool_input": input,
	})
	return string(text)
}

func (c cli) hookCall(agent, call string) (status int, reason, stderr string) {
	c.t.Helper()
	c.stdin = call
	status, stdout, stderr := c.run("hook", agent, "pre-tool")
	if stdout != "" {
		var out struct {
			HookSpecificOutput struct {
				PermissionDecision       string `json:"permissionDecision"`
				PermissionDecisionReason string `json:"permissionDecisionReason"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal([]byte(stdout), &out); err != nil || out.HookSpecificOutput.PermissionDecision != "deny" || out.HookSpecificOutput.PermissionDecisionReason == "" {
			c.t.Fatalf("stdout %q is not a deny verdict with a reason", stdout)
		}
		reason = out.HookSpecificOutput.PermissionDecisionReason
	}
	return status, reason, stderr
}

func auditActions(t *testing.T) []string {
	t.Helper()
	text, err := os.ReadFile(filepath.Join(os.Getenv("OPENPLEXER_HOME"), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, line := range strings.Split(strings.TrimSpace(string(text)), "\n") {
		var record struct {
			Action         string         `json:"action"`
			AgentSessionID string         `json:"agent_session_id"`
			Meta           map[string]any `json:"meta"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record.Action != "policy.loaded" {
			actions = append(actions, record.Action+" "+record.Meta["agent"].(string)+" "+record.AgentSessionID)
		}
	}
	return actions
}

func loadedCLI(t *testing.T) cli {
	t.Helper()
	c := newCLI(t)
	keys, message := signedPolicy(t, testBundle)
	c.must(exitOK, "configure", "--keys", keys, "--member", "mem-dev")
	c.must(exitOK, "policy", "load", message)
	return c
}

func TestHookEnforcesTheSignedPolicyForEachAgent(t *testing.T) {
	c := loadedCLI(t)
	root := project(t)
	for _, step := range []struct {
		agent, call string
		denied      string
	}{
		{"claude-code", callJSON("Write", root, map[string]string{"file_path": filepath.Join(root, "src", "a.go"), "content": "x"}), ""},
		{"claude-code", callJSON("Write", root, map[string]string{"file_path": "docs/a.md", "content": "x"}), "denied a write to docs/a.md"},
		{"claude-code", callJSON("Bash", root, map[string]string{"command": "go test ./..."}), ""},
		{"claude-code", callJSON("Bash", root, map[string]string{"command": "go vet ./..."}), `denied the command "go vet ./..."`},
		{"claude-code", callJSON("Read", root, map[string]string{"file_path": "docs/a.md"}), ""},
		{"codex", callJSON("apply_patch", root, map[string]string{"command": "*** Begin Patch\n*** Add File: docs/b.md\n+x\n*** End Patch"}), "denied a write to docs/b.md"},
		{"codex", callJSON("Bash", root, map[string]string{"command": "go test ./..."}), ""},
	} {
		status, reason, stderr := c.hookCall(step.agent, step.call)
		if status != 0 || stderr != "" || !strings.Contains(reason, step.denied) || (step.denied == "") != (reason == "") {
			t.Errorf("%s %s: exit %d, reason %q, stderr %q, want exit 0 and a denial that says %q", step.agent, step.call, status, reason, stderr, step.denied)
		}
	}
	want := []string{
		"policy.allowed claude-code ses-1", "policy.denied claude-code ses-1", "policy.allowed claude-code ses-1",
		"policy.denied claude-code ses-1", "policy.denied codex ses-1", "policy.allowed codex ses-1",
	}
	if got := auditActions(t); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("audit records:\n%s\nwant one for each call that made a query:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestHookWithNoVerifiedPolicyBlocksEveryJudgedCall(t *testing.T) {
	root := project(t)
	write := callJSON("Write", root, map[string]string{"file_path": "src/a.go", "content": "x"})
	shell := callJSON("Bash", root, map[string]string{"command": "ls"})

	c := newCLI(t)
	for _, call := range []string{write, shell} {
		if status, reason, _ := c.hookCall("claude-code", call); status != 0 || !strings.Contains(reason, "no policy loaded") || !strings.Contains(reason, "openplexer policy load") {
			t.Errorf("with no policy: exit %d, reason %q, want a denial that says how to load one", status, reason)
		}
	}
	if status, reason, _ := c.hookCall("claude-code", callJSON("Read", root, map[string]string{"file_path": "src/a.go"})); status != 0 || reason != "" {
		t.Errorf("a tool that the hook does not gate: exit %d, reason %q, want it allowed", status, reason)
	}
	if got := auditActions(t); len(got) != 2 || !strings.HasPrefix(got[0], "policy.unenforced") {
		t.Errorf("audit records %q, want two policy.unenforced records", got)
	}

	c = loadedCLI(t)
	other, _ := signedPolicy(t, testBundle)
	c.must(exitOK, "configure", "--keys", other)
	if status, reason, _ := c.hookCall("codex", shell); status != 0 || !strings.Contains(reason, "cannot verify its policy") {
		t.Errorf("with a policy that no longer verifies: exit %d, reason %q, want a denial that names the problem", status, reason)
	}

	c = newCLI(t)
	if err := os.MkdirAll(os.Getenv("OPENPLEXER_HOME"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("OPENPLEXER_HOME"), "config.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status, reason, _ := c.hookCall("claude-code", write); status != 0 || !strings.Contains(reason, "cannot read its configuration") {
		t.Errorf("with a configuration that cannot be read: exit %d, reason %q, want a denial that says so", status, reason)
	}
}

func TestHookBlocksAWriteThatTheGateCannotRecord(t *testing.T) {
	c := loadedCLI(t)
	audit := filepath.Join(os.Getenv("OPENPLEXER_HOME"), "audit.jsonl")
	if err := os.Remove(audit); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(audit, 0o700); err != nil {
		t.Fatal(err)
	}
	root := project(t)
	call := callJSON("Write", root, map[string]string{"file_path": "src/a.go", "content": "x"})
	if status, reason, _ := c.hookCall("claude-code", call); status != 0 || !strings.Contains(reason, "could not record") {
		t.Fatalf("exit %d, reason %q, want a denial because the allow was not recorded", status, reason)
	}
}

func TestHookBlocksWritesThatCouldUndoIt(t *testing.T) {
	c := loadedCLI(t)
	root := project(t)
	gateHome := os.Getenv("OPENPLEXER_HOME")
	claudeUser := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "settings.json")
	for _, step := range []struct{ name, agent, call string }{
		{"a Write of the stored policy", "claude-code", callJSON("Write", root, map[string]string{"file_path": filepath.Join(gateHome, "policy.json"), "content": "{}"})},
		{"an Edit of the stored policy", "claude-code", callJSON("Edit", root, map[string]string{"file_path": filepath.Join(gateHome, "policy.json"), "old_string": "a", "new_string": "b"})},
		{"a NotebookEdit of a notebook in the home of OpenPlexer", "claude-code", callJSON("NotebookEdit", root, map[string]string{"notebook_path": filepath.Join(gateHome, "notes.ipynb"), "new_source": "x"})},
		{"a shell write of the key set", "claude-code", callJSON("Bash", root, map[string]string{"command": "cp keys.json " + filepath.Join(gateHome, "keys.json")})},
		{"a Write of the user settings of Claude", "claude-code", callJSON("Write", root, map[string]string{"file_path": claudeUser, "content": "{}"})},
		{"a patch that adds a file to the home of OpenPlexer", "codex", callJSON("apply_patch", root, map[string]string{"command": "*** Begin Patch\n*** Add File: " + filepath.Join(gateHome, "policy.json") + "\n+{}\n*** End Patch"})},
		{"a patch of the project hooks of Codex", "codex", callJSON("apply_patch", root, map[string]string{"command": "*** Begin Patch\n*** Delete File: .codex/hooks.json\n*** End Patch"})},
		{"a shell write of the user hooks of Codex", "codex", callJSON("Bash", root, map[string]string{"command": "rm " + filepath.Join(os.Getenv("CODEX_HOME"), "hooks.json")})},
	} {
		if status, reason, _ := c.hookCall(step.agent, step.call); status != 0 || !strings.Contains(reason, "OpenPlexer blocked a write to") {
			t.Errorf("%s: exit %d, reason %q, want a denial before the policy", step.name, status, reason)
		}
	}
}

func TestHookPlacesPathsInTheProjectRootAboveTheWorkingDirectory(t *testing.T) {
	c := loadedCLI(t)
	root := project(t)
	web := filepath.Join(root, "web")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		name, agent, call, denied string
	}{
		{"claude-code Write of an absolute path outside src", "claude-code", callJSON("Write", web, map[string]string{"file_path": filepath.Join(root, "api", "server.ts"), "content": "x"}), "denied a write to api/server.ts"},
		{"claude-code rm that climbs out of the working directory", "claude-code", callJSON("Bash", web, map[string]string{"command": "rm ../api/server.ts"}), "denied a write to api/server.ts"},
		{"codex rm that climbs out of the working directory", "codex", callJSON("Bash", web, map[string]string{"command": "rm ../api/server.ts"}), "denied a write to api/server.ts"},
		{"claude-code Write that climbs into src", "claude-code", callJSON("Write", web, map[string]string{"file_path": "../src/a.go", "content": "x"}), ""},
		{"claude-code Write of a relative path in the working directory", "claude-code", callJSON("Write", web, map[string]string{"file_path": "a.ts", "content": "x"}), "denied a write to web/a.ts"},
	} {
		status, reason, stderr := c.hookCall(step.agent, step.call)
		if status != 0 || stderr != "" || !strings.Contains(reason, step.denied) || (step.denied == "") != (reason == "") {
			t.Errorf("%s: exit %d, reason %q, stderr %q, want exit 0 and a denial that says %q", step.name, status, reason, stderr, step.denied)
		}
	}
}

func TestHookBlocksAWriteWhenItFindsNoProjectRoot(t *testing.T) {
	c := loadedCLI(t)
	loose := filepath.Join(os.Getenv("HOME"), "loose")
	if err := os.MkdirAll(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	call := callJSON("Write", loose, map[string]string{"file_path": "src/a.go", "content": "x"})
	if status, reason, _ := c.hookCall("claude-code", call); status != 0 || !strings.Contains(reason, "no project root") {
		t.Fatalf("exit %d, reason %q, want a denial that names the missing project root", status, reason)
	}
}

func TestHookRefusesInputThatItCannotRead(t *testing.T) {
	c := loadedCLI(t)
	if status, reason, stderr := c.hookCall("claude-code", "not json"); status != 2 || reason != "" || !strings.HasPrefix(stderr, "openplexer: ") {
		t.Fatalf("exit %d, reason %q, stderr %q, want exit 2 with the reason on standard error", status, reason, stderr)
	}
}

func TestHookThatRunsPastItsBudgetBlocksTheCall(t *testing.T) {
	budget, end := hookBudget, abort
	defer func() { hookBudget, abort = budget, end }()
	stdin, never := io.Pipe()
	aborted := make(chan bool, 1)
	hookBudget = 10 * time.Millisecond
	abort = func() {
		aborted <- true
		never.Close()
	}
	var stdout, stderr bytes.Buffer
	run([]string{"hook", "codex", "pre-tool"}, stdin, &stdout, &stderr, time.Now)
	if len(aborted) != 1 || !strings.Contains(stderr.String(), "did not answer") {
		t.Fatalf("aborted %d times, stderr %q, want the hook to end itself with a reason", len(aborted), stderr.String())
	}
}
