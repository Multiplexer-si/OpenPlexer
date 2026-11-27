package hook

import (
	"strings"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/policy"
)

func TestInProject(t *testing.T) {
	for path, want := range map[string]bool{"web/a.ts": true, "": true, "/etc/x": false, "C:/x": false, "..": false, "../x": false, "..x": true} {
		if got := inProject(path); got != want {
			t.Errorf("inProject(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestQuotedSeparatorInAGrantedCommandIsDenied(t *testing.T) {
	env := Env{Repo: "app", Member: "m", ExecGated: true}
	bundle := &policy.Bundle{Roles: map[string]policy.Role{"r": {Exec: []string{"git commit"}}}, Members: map[string]string{"m": "r"}}
	judge := FirstDeny(func(q policy.Query) policy.Result { return policy.Evaluate(bundle, q) })
	call := []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"/p","tool_input":{"command":"git commit -m \"fix; tidy\""}}`)
	v := Decide(call, env, judge, Tools{"Bash": ShellTool})
	if v.Outcome != Deny || !strings.Contains(v.Reason, `"git commit -m fix; tidy"`) {
		t.Fatalf("verdict = %+v, want a denial that names the command", v)
	}
}

func TestDecideRefusesWhatItCannotRead(t *testing.T) {
	tools := Tools{"Write": FileTool("file_path")}
	allow := FirstDeny(func(policy.Query) policy.Result { return policy.Result{Decision: policy.Allow} })
	for name, input := range map[string]string{
		"input that is not JSON":     `not json`,
		"a call with no cwd":         `{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"/p/a"}}`,
		"a call with no file_path":   `{"hook_event_name":"PreToolUse","tool_name":"Write","cwd":"/p","tool_input":{}}`,
		"a file_path that is a list": `{"hook_event_name":"PreToolUse","tool_name":"Write","cwd":"/p","tool_input":{"file_path":["a"]}}`,
		"a tool_input that is text":  `{"hook_event_name":"PreToolUse","tool_name":"Write","cwd":"/p","tool_input":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if v := Decide([]byte(input), Env{}, allow, tools); v.Outcome != Refuse || v.Reason == "" {
				t.Fatalf("verdict = %+v, want a refusal with a reason", v)
			}
		})
	}
}

func TestDecideAllowsWhatItDoesNotGate(t *testing.T) {
	tools := Tools{"Write": FileTool("file_path")}
	deny := FirstDeny(func(policy.Query) policy.Result { return policy.Result{Decision: policy.Deny, Rule: "no"} })
	for name, input := range map[string]string{
		"a tool that is not in the table": `{"hook_event_name":"PreToolUse","tool_name":"Read","cwd":"/p","tool_input":{"file_path":"/p/a"}}`,
		"an event other than PreToolUse":  `{"hook_event_name":"PostToolUse","tool_name":"Write","cwd":"/p","tool_input":{"file_path":"/p/a"}}`,
		"a write outside the project":     `{"hook_event_name":"PreToolUse","tool_name":"Write","cwd":"/p","tool_input":{"file_path":"/tmp/a"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if v := Decide([]byte(input), Env{}, deny, tools); v.Outcome != Allow {
				t.Fatalf("verdict = %+v, want allow", v)
			}
		})
	}
}

func TestRender(t *testing.T) {
	if out := Render(Verdict{}); out != (Output{}) {
		t.Errorf("an allowed call has output %+v", out)
	}
	deny := Render(Verdict{Outcome: Deny, Reason: `a "quoted" reason`})
	if deny.ExitCode != 0 || !strings.Contains(deny.Stdout, `"permissionDecision":"deny"`) || !strings.Contains(deny.Stdout, `a \"quoted\" reason`) {
		t.Errorf("a denied call has output %+v", deny)
	}
	if refuse := Render(Verdict{Outcome: Refuse, Reason: "x"}); refuse.ExitCode != 2 || refuse.Stdout != "" || refuse.Stderr == "" {
		t.Errorf("a refused call has output %+v", refuse)
	}
}

func TestParseCallUsesExactFinalMembers(t *testing.T) {
	call, err := ParseCall([]byte(`{"session_id":"old","session_id":"current","Session_ID":"ignored","tool_input":{"old":"ignored"},"tool_input":{"file_path":"new"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if call.Session != "current" || len(call.Input) != 1 {
		t.Fatalf("call = %+v", call)
	}
	if path, err := call.Text("file_path"); path != "new" || err != nil {
		t.Fatalf("file_path = %q, %v", path, err)
	}
}

func TestPortableHookRejectsDuplicateMembers(t *testing.T) {
	agent := Agent{Tools: Tools{"Write": FileTool("file_path")}}
	for name, input := range map[string]string{
		"tool name":   `{"hook_event_name":"PreToolUse","tool_name":"Write","tool_name":"Read"}`,
		"file path":   `{"hook_event_name":"PreToolUse","tool_name":"Write","cwd":"/p","tool_input":{"file_path":"a","file_path":"b"}}`,
		"deep member": `{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"unused":[{"nested":{"x":1,"\u0078":2}}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			checked := false
			out := agent.HandleProject([]byte(input), func(Call, Effect) Verdict {
				checked = true
				return Verdict{}
			})
			if checked || out.ExitCode != 2 || out.Stdout != "" || !strings.Contains(out.Stderr, "duplicate member") {
				t.Fatalf("checked = %v, output = %+v, want a blocking exit for duplicate input", checked, out)
			}
		})
	}
	input := []byte(`{"hook_event_name":"PreToolUse","tool_name":"Write","cwd":"/p","tool_input":{"file_path":"a","unused":[{"x":1},{"x":2}]}}`)
	checked := false
	out := agent.HandleProject(input, func(Call, Effect) Verdict {
		checked = true
		return Verdict{}
	})
	if !checked || out != (Output{}) {
		t.Fatalf("checked = %v, output = %+v, want to allow the same name in separate objects", checked, out)
	}
}
