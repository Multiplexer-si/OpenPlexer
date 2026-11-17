package hook

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/policy"
)

func TestAWriteToAGuardedPathIsDeniedBeforeThePolicy(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	root := filepath.Join(base, "proj")
	gate := filepath.Join(home, ".openplexer")
	mkdirs(t, filepath.Join(root, ".git"), filepath.Join(root, "web"), gate)
	env := Env{
		Root: func(cwd string) (string, bool) { return FindRoot(cwd, home) },
		Home: home,
		Guards: []Guard{
			{Path: gate, Why: "it holds the policy of OpenPlexer"},
			{Path: ".claude/settings.json", Why: "it holds the hook of OpenPlexer"},
		},
	}
	allow := func(policy.Query, ...policy.Query) Verdict { return Verdict{} }
	tools := Tools{"Write": FileTool("file_path"), "Bash": ShellTool, "apply_patch": func(call Call) (Effect, error) {
		text, err := call.Text("command")
		return Effect{Writes: []string{text}}, err
	}}
	web := filepath.Join(root, "web")
	input := func(tool, field, value string) []byte {
		return []byte(`{"hook_event_name":"PreToolUse","tool_name":"` + tool + `","cwd":"` + web + `","tool_input":{"` + field + `":"` + value + `"}}`)
	}
	for _, c := range []struct {
		name  string
		input []byte
		want  string
	}{
		{"Write of a file in the gate home", input("Write", "file_path", filepath.Join(gate, "policy.json")), "policy of OpenPlexer"},
		{"Write of the gate home itself", input("Write", "file_path", gate), "policy of OpenPlexer"},
		{"a shell redirection into the gate home", input("Bash", "command", "echo {} > "+filepath.Join(gate, "keys.json")), "policy of OpenPlexer"},
		{"a shell write through ~", input("Bash", "command", "rm ~/.openplexer/config.json"), "policy of OpenPlexer"},
		{"a relative write that climbs into the gate home", input("Bash", "command", "touch ../../home/.openplexer/policy.json"), "policy of OpenPlexer"},
		{"a patch of a file in the gate home", input("apply_patch", "command", filepath.Join(gate, "audit.jsonl")), "policy of OpenPlexer"},
		{"Write of the project settings of the agent", input("Write", "file_path", "../.claude/settings.json"), "hook of OpenPlexer"},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := Decide(c.input, env, allow, tools)
			if v.Outcome != Deny || !strings.Contains(v.Reason, c.want) {
				t.Fatalf("verdict = %+v, want a denial that says %q", v, c.want)
			}
		})
	}
	for _, c := range []struct {
		name  string
		input []byte
	}{
		{"a write next to the gate home", input("Write", "file_path", filepath.Join(home, ".openplexer-notes"))},
		{"a write of another file of the project", input("Write", "file_path", "a.ts")},
		{"a settings file in a subdirectory", input("Write", "file_path", ".claude/settings.json")},
	} {
		t.Run(c.name, func(t *testing.T) {
			if v := Decide(c.input, env, allow, tools); v.Outcome != Allow {
				t.Fatalf("verdict = %+v, want allow", v)
			}
		})
	}
}
