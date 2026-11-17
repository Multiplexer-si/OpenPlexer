package adapter_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/adapter"
	"github.com/multiplexer-si/openplexer/internal/adapter/hook"
)

func TestTheEntryOfEachAgentIsTheOneTheGoldensPin(t *testing.T) {
	for _, agent := range adapter.Agents {
		t.Run(agent.Name, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("hook", "testdata", "install", agent.Name, "missing", "installed.json"))
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := hook.Install(nil, false, agent.Handler("/usr/local/bin/openplexer"))
			if err != nil || string(got) != string(want) {
				t.Fatalf("install into a missing file gave %s (%v), want %s", got, err, want)
			}
		})
	}
}

func TestAnExecutablePathWithASpaceIsQuoted(t *testing.T) {
	agent, _ := adapter.Lookup("codex")
	if got, want := agent.Handler("/Users/a b/bin/openplexer").Command, `'/Users/a b/bin/openplexer' hook codex pre-tool`; got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestHookFilesPerScope(t *testing.T) {
	getenv := func(name string) string {
		if name == "CODEX_HOME" {
			return "/env/codex"
		}
		return ""
	}
	for _, c := range []struct {
		agent string
		scope hook.Scope
		want  string
	}{
		{"claude-code", hook.User, "/home/a/.claude/settings.json"},
		{"claude-code", hook.Project, "/work/p/.claude/settings.json"},
		{"codex", hook.User, "/env/codex/hooks.json"},
		{"codex", hook.Project, "/work/p/.codex/hooks.json"},
	} {
		agent, ok := adapter.Lookup(c.agent)
		if !ok {
			t.Fatalf("no agent %q", c.agent)
		}
		if got := agent.File(c.scope, agent.UserDir(getenv, "/home/a"), "/work/p"); got != c.want {
			t.Errorf("%s %s file = %q, want %q", c.agent, c.scope, got, c.want)
		}
	}
}
