package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/policy"
)

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFindRoot(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	mkdirs(t, filepath.Join(repo, ".git"), filepath.Join(repo, "web", ".claude"), filepath.Join(home, ".claude"))
	worktree := filepath.Join(home, "worktree")
	mkdirs(t, filepath.Join(worktree, "src"))
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(home, "bare", "sub")
	mkdirs(t, filepath.Join(home, "bare", ".codex"), bare)
	nothing := filepath.Join(home, "nothing", "sub")
	mkdirs(t, nothing)

	for _, c := range []struct {
		name, cwd, want string
	}{
		{"the nearest .git above cwd", filepath.Join(repo, "web"), repo},
		{"a .claude below the .git does not move the root", filepath.Join(repo, "web"), repo},
		{"a .git file of a worktree", filepath.Join(worktree, "src"), worktree},
		{"an agent marker when there is no .git", bare, filepath.Join(home, "bare")},
		{"the home directory is never a root", nothing, ""},
		{"cwd is the root", repo, repo},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := FindRoot(c.cwd, home)
			if got != c.want || ok != (c.want != "") {
				t.Fatalf("FindRoot(%q) = %q, %v, want %q", c.cwd, got, ok, c.want)
			}
		})
	}
}

func recordQueries(asked *[]string) Judge {
	return func(first policy.Query, rest ...policy.Query) Verdict {
		for _, q := range append([]policy.Query{first}, rest...) {
			*asked = append(*asked, string(q.Action)+" "+q.Path+q.Command)
		}
		return Verdict{Deny, "denied"}
	}
}

func TestPathsAreMeasuredFromTheRootWhenCwdIsBelowIt(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "proj")
	mkdirs(t, filepath.Join(root, ".git"), filepath.Join(root, "web"), filepath.Join(root, "api"))
	env := Env{Root: func(cwd string) (string, bool) { return FindRoot(cwd, home) }}
	tools := Tools{"Write": FileTool("file_path"), "Bash": ShellTool}
	web := filepath.Join(root, "web")
	for _, c := range []struct{ name, input, want string }{
		{"a write named by its absolute path", `{"tool_name":"Write","tool_input":{"file_path":"` + filepath.Join(root, "api", "server.ts") + `"}}`, "write api/server.ts"},
		{"a shell write that climbs out of cwd", `{"tool_name":"Bash","tool_input":{"command":"rm ../api/server.ts"}}`, "write api/server.ts"},
	} {
		t.Run(c.name, func(t *testing.T) {
			input := strings.Replace(c.input, "{", `{"hook_event_name":"PreToolUse","cwd":"`+web+`",`, 1)
			var asked []string
			v := Decide([]byte(input), env, recordQueries(&asked), tools)
			if v.Outcome != Deny || len(asked) != 1 || asked[0] != c.want {
				t.Fatalf("verdict %+v after queries %q, want a denial after %q", v, asked, c.want)
			}
		})
	}
}

func TestWithNoRootEveryWriteAndGatedCommandIsDenied(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "loose", "dir")
	mkdirs(t, cwd)
	never := func(policy.Query, ...policy.Query) Verdict {
		t.Fatal("the policy was asked about a call with no project root")
		return Verdict{}
	}
	tools := Tools{"Write": FileTool("file_path"), "Bash": ShellTool}
	call := func(tool, field, value string) []byte {
		return []byte(`{"hook_event_name":"PreToolUse","tool_name":"` + tool + `","cwd":"` + cwd + `","tool_input":{"` + field + `":"` + value + `"}}`)
	}
	root := func(cwd string) (string, bool) { return FindRoot(cwd, home) }
	for _, c := range []struct {
		name      string
		input     []byte
		execGated bool
		want      Outcome
	}{
		{"a write inside cwd", call("Write", "file_path", "a.txt"), false, Deny},
		{"a write outside cwd", call("Write", "file_path", "/tmp/a.txt"), false, Deny},
		{"a shell write", call("Bash", "command", "rm ../x"), false, Deny},
		{"a command when the role lists commands", call("Bash", "command", "ls"), true, Deny},
		{"a command when the role lists none", call("Bash", "command", "ls"), false, Allow},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := Decide(c.input, Env{Root: root, ExecGated: c.execGated}, never, tools)
			if v.Outcome != c.want || (c.want == Deny && !strings.Contains(v.Reason, "project root")) {
				t.Fatalf("verdict = %+v, want %v with a reason that names the project root", v, c.want)
			}
		})
	}
}
