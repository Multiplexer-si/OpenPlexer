package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func scenarioNamed(t *testing.T, name string) scenario {
	t.Helper()
	for _, s := range scenarios {
		if s.name == name {
			return s
		}
	}
	t.Fatalf("no scenario %q", name)
	return scenario{}
}

func passing(s scenario) *result {
	res := &result{
		Audit:        []auditEntry{{Action: s.want.action, Query: s.want.query, State: s.want.state, Target: s.want.target}},
		TargetOnDisk: s.wantOnDisk,
	}
	if s.reason != "" {
		res.ReasonReachedAgent = true
	}
	if s.offline {
		blocked := true
		res.NetworkBlocked = &blocked
	}
	return res
}

func TestJudgePassesEachScenarioThatMeetsItsWant(t *testing.T) {
	for _, s := range scenarios {
		res := passing(s)
		judge(res, s)
		if len(res.Problems) != 0 {
			t.Errorf("%s: %v", s.name, res.Problems)
		}
	}
}

func TestJudgeFailsWhatTheScenarioDoesNotMeet(t *testing.T) {
	deny := scenarioNamed(t, "deny_write")
	for name, c := range map[string]struct {
		s      scenario
		mutate func(*result)
	}{
		"no audit record":        {deny, func(r *result) { r.Audit = nil }},
		"wrong policy state":     {deny, func(r *result) { r.Audit[0].State = "stale" }},
		"wrong action":           {deny, func(r *result) { r.Audit[0].Action = "policy.allowed" }},
		"file on disk":           {deny, func(r *result) { r.TargetOnDisk = true }},
		"reason not delivered":   {deny, func(r *result) { r.ReasonReachedAgent = false }},
		"another command denied": {scenarioNamed(t, "deny_shell"), func(r *result) { r.Audit[0].Target = commandTarget("touch other") }},
		"opposite decision": {scenarioNamed(t, "allow_write"), func(r *result) {
			r.Audit = append(r.Audit, auditEntry{Action: "policy.denied", Query: "write", State: "verified", Target: "allowed.txt"})
		}},
		"offline without network control": {scenarioNamed(t, "offline"), func(r *result) { r.NetworkBlocked = nil }},
		"offline with open network": {scenarioNamed(t, "offline"), func(r *result) {
			open := false
			r.NetworkBlocked = &open
		}},
	} {
		res := passing(c.s)
		c.mutate(res)
		judge(res, c.s)
		if len(res.Problems) == 0 {
			t.Errorf("%s: judge passed", name)
		}
	}
}

func TestJudgeIgnoresOtherTargetsWhenItChecksForAContradiction(t *testing.T) {
	s := scenarioNamed(t, "allow_write")
	res := passing(s)
	res.Audit = append(res.Audit, auditEntry{Action: "policy.denied", Query: "exec", State: "verified", Target: "sha256:other"})
	judge(res, s)
	if len(res.Problems) != 0 {
		t.Fatalf("a denied command is not a contradiction of an allowed write: %v", res.Problems)
	}
}

func TestReadAuditKeepsDecisionsAndSkipsPolicyLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	lines := []string{
		`{"id":"1","action":"policy.loaded","target":"prj-x","reason":null,"meta":{"version":1}}`,
		`{"id":"2","action":"policy.denied","target":"sentinel/x.txt","reason":"rule one","meta":{"agent":"codex","action":"write","policy_state":"stale"}}`,
		`{"id":"3","action":"policy.unenforced","target":"sha256:00","reason":null,"meta":{"agent":"claude-code","action":"exec","policy_state":"absent"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []auditEntry{
		{Action: "policy.denied", Target: "sentinel/x.txt", Reason: "rule one", Agent: "codex", Query: "write", State: "stale"},
		{Action: "policy.unenforced", Target: "sha256:00", Agent: "claude-code", Query: "exec", State: "absent"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if got, err := readAudit(filepath.Join(t.TempDir(), "missing")); err != nil || got != nil {
		t.Fatalf("a missing log is empty, got %v, %v", got, err)
	}
}

func TestTOMLRendersAHookFileValue(t *testing.T) {
	var file struct {
		Hooks map[string]any `json:"hooks"`
	}
	text := `{"hooks":{"PreToolUse":[{"matcher":"Bash|apply_patch","hooks":[{"type":"command","command":"/a b/openplexer hook codex pre-tool --say \"hi\"","timeout":5,"async":false}]}]}}`
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	if err := dec.Decode(&file); err != nil {
		t.Fatal(err)
	}
	got, err := toml(file.Hooks["PreToolUse"])
	if err != nil {
		t.Fatal(err)
	}
	want := `[{hooks=[{async=false,command="/a b/openplexer hook codex pre-tool --say \"hi\"",timeout=5,type="command"}],matcher="Bash|apply_patch"}]`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if _, err := toml(nil); err == nil {
		t.Fatal("null has no TOML form")
	}
}

func TestHooksOverridesWrapsEachCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	text := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/bin/op hook codex pre-tool","timeout":5}]}]}}`
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	plain, err := hooksOverrides(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"-c", `hooks.PreToolUse=[{hooks=[{command="/bin/op hook codex pre-tool",timeout=5,type="command"}],matcher="Bash"}]`}; !slices.Equal(plain, want) {
		t.Fatalf("got %q", plain)
	}
	wrapped, err := hooksOverrides(path, offlineCommand)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wrapped[1], `command="/usr/bin/sandbox-exec -p '`+offlineProfile+`' /bin/op hook codex pre-tool"`) {
		t.Fatalf("the command is not wrapped: %q", wrapped)
	}
}

func TestOfflineCommandQuotesTheProfile(t *testing.T) {
	got := offlineCommand("/bin/op hook claude-code pre-tool")
	want := "/usr/bin/sandbox-exec -p '(version 1)(allow default)(deny network*)' /bin/op hook claude-code pre-tool"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestOfflineClaudeSettingsReplacesOnlyTheInstalledCommand(t *testing.T) {
	settings := []byte(`{
  "sandbox": {"enabled": true},
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash|Write", "hooks": [{"type": "command", "command": "/tmp/bin/openplexer hook claude-code pre-tool", "timeout": 5}]}
    ]
  }
}`)
	got, err := offlineClaudeSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	command := "/tmp/bin/openplexer hook claude-code pre-tool"
	want := bytes.Replace(settings, []byte(command), []byte(offlineCommand(command)), 1)
	if !bytes.Equal(got, want) {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("the rewritten file is not JSON: %v", err)
	}
}

func TestOfflineClaudeSettingsFailsWithoutExactlyOneCommand(t *testing.T) {
	if _, err := offlineClaudeSettings([]byte(`{"sandbox":{"enabled":true}}`)); err == nil {
		t.Fatal("settings with no installed hook must fail")
	}
	twice := `{"hooks":{"PreToolUse":[{"hooks":[{"command":"/b/op hook claude-code pre-tool"}]},{"hooks":[{"command":"/b/op hook claude-code pre-tool"}]}]}}`
	if _, err := offlineClaudeSettings([]byte(twice)); err == nil {
		t.Fatal("a command that appears twice must fail")
	}
}

func TestFingerprintSeesEditsAdditionsRemovalsAndRetargetedLinks(t *testing.T) {
	dir := t.TempDir()
	file, other, link := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json"), filepath.Join(dir, "link")
	gone := filepath.Join(dir, "gone.json")
	for path, text := range map[string]string{file: "1", other: "2", gone: "3"} {
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	paths := []string{dir, filepath.Join(t.TempDir(), "absent")}
	before, err := fingerprint(paths)
	if err != nil {
		t.Fatal(err)
	}
	if got := changed(before, before); len(got) != 0 {
		t.Fatalf("an untouched tree changed: %v", got)
	}
	added := filepath.Join(dir, "new.json")
	for _, f := range []func() error{
		func() error { return os.WriteFile(file, []byte("edited"), 0o600) },
		func() error { return os.WriteFile(added, []byte("x"), 0o600) },
		func() error { return os.Remove(gone) },
		func() error { os.Remove(link); return os.Symlink(other, link) },
	} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	after, err := fingerprint(paths)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{added, file, gone, link}
	slices.Sort(want)
	if got := changed(before, after); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFingerprintDoesNotFollowASymlinkedDirectory(t *testing.T) {
	outside, dir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	got, err := fingerprint([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[filepath.Join(dir, "link", "f")]; ok {
		t.Fatalf("walked through a symlink: %v", got)
	}
}

func TestSignPolicyMakesAKeySetAndAMessage(t *testing.T) {
	keys, message, err := signPolicy(testBundle)
	if err != nil {
		t.Fatal(err)
	}
	var set struct {
		Keys []map[string]string `json:"keys"`
	}
	var msg struct {
		Signature string `json:"signature"`
		Bundle    struct {
			ProjectID string `json:"project_id"`
		} `json:"bundle"`
	}
	if json.Unmarshal(keys, &set) != nil || len(set.Keys) != 1 || set.Keys[0]["crv"] != "P-256" {
		t.Fatalf("key set %s", keys)
	}
	if json.Unmarshal(message, &msg) != nil || strings.Count(msg.Signature, ".") != 2 || msg.Bundle.ProjectID != projectID {
		t.Fatalf("message %s", message)
	}
}

func TestFeedbackKeepsHookEchoesOutOfWhatTheAgentSaw(t *testing.T) {
	claudeOut := `{"type":"system","subtype":"hook_response","output":"TOKEN"}
{"type":"user","message":{"content":[{"type":"tool_result","content":"hook error: TOKEN"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"said TOKEN"}]}}`
	said, tools := claudeFeedback([]byte(claudeOut), []byte("stderr TOKEN"))
	if !strings.Contains(said, "said TOKEN") || strings.Contains(said, "hook_response") || !strings.Contains(tools, "hook error: TOKEN") || strings.Contains(tools, "stderr") {
		t.Fatalf("claude: said %q tools %q", said, tools)
	}
	codexOut := `{"type":"item.completed","item":{"type":"agent_message","text":"said TOKEN"}}
{"type":"item.completed","item":{"type":"error","message":"bypass notice TOKEN"}}
{"type":"item.completed","item":{"type":"command_execution","aggregated_output":"blocked: TOKEN"}}`
	said, tools = codexFeedback([]byte(codexOut), []byte("Command blocked by PreToolUse hook: REASON. Command: x\n"))
	if !strings.Contains(said, "said TOKEN") || strings.Contains(said, "bypass") || !strings.Contains(tools, "blocked: TOKEN") || strings.Contains(tools, "bypass") {
		t.Fatalf("codex: said %q tools %q", said, tools)
	}
	if !strings.Contains(tools, "Command blocked by PreToolUse hook: REASON") || strings.Contains(said, "REASON") {
		t.Fatalf("codex stderr must reach the tool results only: said %q tools %q", said, tools)
	}
}

func TestFeedbackKeepsTheQuotesOfAReason(t *testing.T) {
	reason := `denied the command "touch x" <&>`
	claudeOut := `{"type":"user","message":{"content":[{"type":"tool_result","content":[{"type":"text","text":"hook error: denied the command \"touch x\" \u003c\u0026\u003e"}]}]}}`
	if _, tools := claudeFeedback([]byte(claudeOut), nil); !strings.Contains(tools, reason) {
		t.Fatalf("claude: tools %q do not hold %q", tools, reason)
	}
	codexOut := `{"type":"item.completed","item":{"type":"command_execution","aggregated_output":"denied the command \"touch x\" <&>"}}`
	if _, tools := codexFeedback([]byte(codexOut), nil); !strings.Contains(tools, reason) {
		t.Fatalf("codex: tools %q do not hold %q", tools, reason)
	}
}

func TestClaudeSlugMatchesTheProjectsDirectoryName(t *testing.T) {
	got := claudeSlug("/private/var/folders/xy/T/realagent-123/project")
	if want := "-private-var-folders-xy-T-realagent-123-project"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
