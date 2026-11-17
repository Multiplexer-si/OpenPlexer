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

const preToolInput = `{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"/p/sentinel/x.txt","content":"ok"}}`

func TestDecideDeniesATokenInTheToolInput(t *testing.T) {
	cfg := stubConfig{DenyTokens: []string{"sentinel/"}, Reason: "no", Style: styleJSON}
	rec := decide(cfg, []byte(preToolInput))
	if rec.Decision != "deny" || rec.Exit != 0 {
		t.Fatalf("got decision %q exit %d", rec.Decision, rec.Exit)
	}
	if !strings.Contains(rec.Stdout, `"permissionDecision":"deny"`) || !strings.Contains(rec.Stdout, `"permissionDecisionReason":"no"`) {
		t.Fatalf("deny output is %s", rec.Stdout)
	}
}

func TestDecideExit2WritesTheReasonToStderr(t *testing.T) {
	cfg := stubConfig{DenyTokens: []string{"sentinel/"}, Reason: "no", Style: styleExit2}
	rec := decide(cfg, []byte(preToolInput))
	if rec.Exit != 2 || rec.Stderr != "no" || rec.Stdout != "" {
		t.Fatalf("got exit %d stderr %q stdout %q", rec.Exit, rec.Stderr, rec.Stdout)
	}
}

func TestDecideAllowsOtherInputsAndOtherEvents(t *testing.T) {
	cfg := stubConfig{DenyTokens: []string{"sentinel/"}, Reason: "no", Style: styleJSON}
	if rec := decide(cfg, []byte(strings.Replace(preToolInput, "sentinel/", "safe/", 1))); rec.Decision != "allow" || rec.Stdout != "" {
		t.Fatalf("a clean input got %+v", rec)
	}
	if rec := decide(cfg, []byte(`{"hook_event_name":"Stop","tool_input":"sentinel/"}`)); rec.Decision != "none" {
		t.Fatalf("a Stop event got %+v", rec)
	}
}

func TestRunStubLogsEveryCallAndKeepsGarbage(t *testing.T) {
	cfg := stubConfig{LogPath: filepath.Join(t.TempDir(), "log.jsonl"), Style: styleJSON}
	var out, errOut bytes.Buffer
	runStub(cfg, strings.NewReader(preToolInput), &out, &errOut)
	runStub(cfg, strings.NewReader("not json"), &out, &errOut)
	recs, err := readLog(cfg.LogPath)
	if err != nil || len(recs) != 2 {
		t.Fatalf("got %d records, err %v", len(recs), err)
	}
	if recs[0].Event != "PreToolUse" || recs[1].Event != "unparseable" {
		t.Fatalf("got events %q and %q", recs[0].Event, recs[1].Event)
	}
}

func TestRedactorRemovesWhatTheRepositoryMustNotHold(t *testing.T) {
	home, _ := os.UserHomeDir()
	host, _ := os.Hostname()
	root := filepath.Join(os.TempDir(), "realagent-123")
	project := filepath.Join(root, "project")
	r, err := newRedactor(root)
	if err != nil {
		t.Fatal(err)
	}
	in := map[string]any{
		"cwd":             project,
		"session_id":      "01a1e91a-aa9d-7df0-ba3e-598d6a6c5ddc",
		"transcript_path": home + "/.claude/projects/x/7b0780fa-d2e7-4493-96df-ca8bff63ce3b.jsonl",
		"started":         "2026-11-17T10:43:27.664255Z",
		"listing":         "drwx------@ 2 u staff 64 Nov 17 18:38 outside",
		"tool_use_id":     "toolu_013aG3idPgsT9Y6UkKeV3Kbv",
		"note":            "see " + claudeSlug(project) + " on " + host,
		"target":          filepath.Join(root, "outside", "escaped.txt"),
	}
	raw, _ := json.Marshal(in)
	out, err := r.json(raw)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, leak := range []string{home, host, "01a1e91a", "7b0780fa", "2026-11-17", "Nov 17 18:38", "toolu_013", "realagent-123"} {
		if strings.Contains(got, leak) {
			t.Errorf("redacted sample still holds %q:\n%s", leak, got)
		}
	}
	for _, want := range []string{"<PROJECT>", "<RUN>/outside/escaped.txt", "<HOME>", "<HOST>", "<SESSION_ID>", "<UUID>", "<TIMESTAMP>", "<TOOL_USE_ID>", "<PROJECT_SLUG>"} {
		if !strings.Contains(got, want) {
			t.Errorf("redacted sample lacks %q:\n%s", want, got)
		}
	}
}

func TestCommittedSamplesHoldNothingToRedact(t *testing.T) {
	r, err := newRedactor(filepath.Join(t.TempDir(), "run"))
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("testdata/*/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no samples found: %v", err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		again, err := r.json(data)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if !bytes.Equal(again, data) {
			t.Errorf("%s changes when redacted again, so it holds an id, a timestamp, or a private path:\n%s", f, again)
		}
	}
}

func TestFeedbackKeepsHookEchoesOutOfWhatTheAgentSaw(t *testing.T) {
	claudeOut := `{"type":"system","subtype":"hook_response","output":"TOKEN"}
{"type":"user","message":{"content":[{"type":"tool_result","content":"hook error: TOKEN"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"said TOKEN"}]}}`
	said, tools := claudeFeedback([]byte(claudeOut))
	if !strings.Contains(said, "said TOKEN") || strings.Contains(said, "hook_response") || !strings.Contains(tools, "hook error: TOKEN") {
		t.Fatalf("claude: said %q tools %q", said, tools)
	}
	codexOut := `{"type":"item.completed","item":{"type":"agent_message","text":"said TOKEN"}}
{"type":"item.completed","item":{"type":"error","message":"bypass notice TOKEN"}}
{"type":"item.completed","item":{"type":"command_execution","aggregated_output":"blocked: TOKEN"}}`
	said, tools = codexFeedback([]byte(codexOut))
	if !strings.Contains(said, "said TOKEN") || strings.Contains(said, "bypass") || !strings.Contains(tools, "blocked: TOKEN") || strings.Contains(tools, "bypass") {
		t.Fatalf("codex: said %q tools %q", said, tools)
	}
}

func TestJudge(t *testing.T) {
	deny := scenario{denyTokens: []string{"x"}}
	ok := &result{PreToolUse: []preToolCall{{Decision: "deny"}}}
	judge(ok, deny)
	if !ok.Pass {
		t.Fatalf("a denied, absent target must pass: %v", ok.Problems)
	}
	written := &result{PreToolUse: []preToolCall{{Decision: "deny"}}, TargetOnDisk: true}
	judge(written, deny)
	if written.Pass {
		t.Fatal("a denied action whose file exists must fail")
	}
	silent := &result{}
	judge(silent, scenario{wantOnDisk: true, target: "x"})
	if silent.Pass {
		t.Fatal("a run in which no hook fired must fail")
	}
	escape := scenario{target: "outside/x", targetTools: 2}
	bothTools := []preToolCall{{Tool: "Write", NamesTarget: true, Decision: "allow"}, {Tool: "Bash", NamesTarget: true, Decision: "allow"}}
	confined := &result{PreToolUse: bothTools}
	judge(confined, escape)
	if !confined.Pass {
		t.Fatalf("two tools that tried the target and an absent file must pass: %v", confined.Problems)
	}
	escaped := &result{PreToolUse: bothTools, TargetOnDisk: true}
	judge(escaped, escape)
	if escaped.Pass {
		t.Fatal("a file outside the project must fail the escape scenario")
	}
	oneTool := &result{PreToolUse: []preToolCall{{Tool: "Bash", Decision: "allow"}, {Tool: "Write", NamesTarget: true, Decision: "allow"}}}
	judge(oneTool, escape)
	if oneTool.Pass {
		t.Fatal("an escape run in which only one tool tried the target proves nothing about the other")
	}
	stubDenied := &result{PreToolUse: []preToolCall{{Tool: "Write", NamesTarget: true, Decision: "deny"}, {Tool: "Bash", NamesTarget: true, Decision: "allow"}}}
	judge(stubDenied, escape)
	if stubDenied.Pass {
		t.Fatal("an escape run that the stub denied proves nothing about the sandbox")
	}
}

func TestSummarizeSeesTheTargetInTheToolInput(t *testing.T) {
	r := record{Event: "PreToolUse", Decision: "allow", Input: json.RawMessage(`{"tool_name":"Bash","tool_input":{"command":"python3 -c 'open(\"../outside/escaped.txt\")'","description":"d"}}`)}
	got := summarize(r, "escaped.txt")
	if got.Tool != "Bash" || !got.NamesTarget || !slices.Equal(got.Fields, []string{"command", "description"}) {
		t.Fatalf("got %+v", got)
	}
	if summarize(r, "other.txt").NamesTarget {
		t.Fatal("a tool input without the target must not name it")
	}
}

func TestRedactorPrefersTheProjectOverTheHomeDirectory(t *testing.T) {
	home, _ := os.UserHomeDir()
	r, err := newRedactor(filepath.Join(home, "scratch"))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.text(filepath.Join(home, "scratch", "project", "a.txt")); got != "<PROJECT>/a.txt" {
		t.Fatalf("got %q", got)
	}
	if got := r.text(filepath.Join(home, "scratch", "outside", "a.txt")); got != "<RUN>/outside/a.txt" {
		t.Fatalf("got %q", got)
	}
}

func TestClaudeSlugMatchesTheProjectsDirectoryName(t *testing.T) {
	got := claudeSlug("/private/var/folders/ws/T/realagent-2054369725/project")
	if want := "-private-var-folders-ws-T-realagent-2054369725-project"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
