package hook

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/policy"
)

func TestMovingAGuardedPathAwayIsDenied(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	root := filepath.Join(base, "proj")
	gate := filepath.Join(home, ".openplexer")
	userSettings := filepath.Join(home, ".claude", "settings.json")
	mkdirs(t, filepath.Join(root, ".git"), filepath.Join(root, "src"), gate)
	env := Env{
		Root: Roots{Home: home}.Find,
		Home: home,
		Guards: []Guard{
			{Path: gate, Why: "it holds the policy of OpenPlexer"},
			{Path: userSettings, Why: "it holds the hook of OpenPlexer"},
			{Path: ".claude/settings.json", Why: "it holds the hook of OpenPlexer"},
		},
	}
	allow := func(policy.Query, ...policy.Query) Verdict { return Verdict{} }
	bash := func(command string) []byte {
		return []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"` + root + `","tool_input":{"command":"` + command + `"}}`)
	}
	for _, command := range []string{
		"mv " + userSettings + " /private/tmp/s.json",
		"mv ~/.claude/settings.json /private/tmp/s.json",
		"mv .claude/settings.json src/s.json",
		"/bin/mv -f .claude/settings.json src/s.json",
		"mv " + filepath.Join(gate, "installs", "0123.json") + " /tmp/r",
		"mv " + filepath.Join(gate, "installs") + " src/i",
		"mv " + gate + " /private/tmp/o",
		"mv src/a.go .claude/settings.json src/b.go src/",
		"mv -t src src/a.go .claude/settings.json",
		"mv --target-directory=src .claude/settings.json",
		"mv -- .claude/settings.json src/s.json",
		"git mv .claude/settings.json src/s.json",
		"git mv src/s.json .claude/settings.json",
		"git mv -k src/a.go .claude/settings.json src/",
		"git -C .claude mv settings.json s.json",
		"git -c core.editor=true mv .claude/settings.json src/s.json",
		"git -C src -C " + root + " mv .claude/settings.json src/s.json",
		"git --attr-source HEAD mv .claude/settings.json src/s.json",
		"git -C $PWD mv " + userSettings + " /private/tmp/s.json",
	} {
		t.Run(command, func(t *testing.T) {
			v := Decide(bash(command), env, allow, Tools{"Bash": ShellTool})
			if v.Outcome != Deny || !strings.Contains(v.Reason, "OpenPlexer blocked a write to") {
				t.Fatalf("verdict = %+v, want the guard to deny the move", v)
			}
		})
	}
	for _, command := range []string{
		"mv src/a.go src/b.go",
		"git mv src/a.go src/b.go",
		"git status",
		"git log -- .claude/settings.json",
	} {
		t.Run(command, func(t *testing.T) {
			if v := Decide(bash(command), env, allow, Tools{"Bash": ShellTool}); v.Outcome != Allow {
				t.Fatalf("verdict = %+v, want allow", v)
			}
		})
	}
}

func TestThePolicyJudgesEveryPathThatAMoveTakesAway(t *testing.T) {
	for command, want := range map[string][]string{
		"mv api/server.ts src/server.ts":     {"src/server.ts", "api/server.ts"},
		"mv api/a.ts api/b.ts src/":          {"src/", "api/a.ts", "api/b.ts"},
		"mv -t src api/a.ts api/b.ts":        {"src", "api/a.ts", "api/b.ts"},
		"git mv api/server.ts src/server.ts": {"api/server.ts", "src/server.ts"},
		"git -C api mv server.ts s.ts":       {"api/server.ts", "api/s.ts"},
		"mv $SRC src/a.ts":                   {"src/a.ts"},
	} {
		t.Run(command, func(t *testing.T) {
			var got []string
			judge := func(first policy.Query, rest ...policy.Query) Verdict {
				for _, q := range append([]policy.Query{first}, rest...) {
					got = append(got, q.Path)
				}
				return Verdict{}
			}
			call := []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"/p","tool_input":{"command":"` + command + `"}}`)
			Decide(call, Env{}, judge, Tools{"Bash": ShellTool})
			if !slices.Equal(got, want) {
				t.Fatalf("write queries = %q, want %q", got, want)
			}
		})
	}
}
