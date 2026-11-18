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

func TestRootsFind(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	mkdirs(t, filepath.Join(repo, ".git"), filepath.Join(repo, "web", ".claude"), filepath.Join(home, ".claude"))
	worktree := filepath.Join(home, "worktree")
	mkdirs(t, filepath.Join(worktree, "src"))
	gitlink := func(dir string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitlink(worktree)
	bare := filepath.Join(home, "bare", "sub")
	mkdirs(t, filepath.Join(home, "bare", ".codex"), bare)
	nothing := filepath.Join(home, "nothing", "sub")
	mkdirs(t, nothing)
	super := filepath.Join(home, "super")
	mkdirs(t, filepath.Join(super, ".git"), filepath.Join(super, "web", "src"), filepath.Join(super, "lib", ".git"))
	gitlink(filepath.Join(super, "web"))
	planted := filepath.Join(home, "planted")
	mkdirs(t, filepath.Join(planted, ".claude"), filepath.Join(planted, "web", ".git"))
	nested := filepath.Join(home, "nested")
	mkdirs(t, filepath.Join(nested, ".codex"), filepath.Join(nested, "web", ".claude"))
	alias := filepath.Join(home, "alias")
	if err := os.Symlink(super, alias); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name, cwd string
		recorded  []string
		want      string
		ambiguous []string
	}{
		{name: "the one .git above cwd", cwd: filepath.Join(repo, "web"), want: repo},
		{name: "a .claude below the .git does not move the root", cwd: filepath.Join(repo, "web"), want: repo},
		{name: "a .git file of a worktree", cwd: filepath.Join(worktree, "src"), want: worktree},
		{name: "an agent marker when there is no .git", cwd: bare, want: filepath.Join(home, "bare")},
		{name: "the home directory is never a root", cwd: nothing},
		{name: "cwd is the root", cwd: repo, want: repo},
		{name: "a submodule with a gitlink file has two candidates", cwd: filepath.Join(super, "web", "src"), ambiguous: []string{filepath.Join(super, "web"), super}},
		{name: "a nested .git directory has two candidates", cwd: filepath.Join(super, "lib"), ambiguous: []string{filepath.Join(super, "lib"), super}},
		{name: "a .git below a directory that only an agent marker names has two candidates", cwd: filepath.Join(planted, "web"), ambiguous: []string{filepath.Join(planted, "web"), planted}},
		{name: "two agent markers and no .git have two candidates", cwd: filepath.Join(nested, "web"), ambiguous: []string{filepath.Join(nested, "web"), nested}},
		{name: "a recorded root wins over a submodule below it", cwd: filepath.Join(super, "web", "src"), recorded: []string{super}, want: super},
		{name: "a recorded root wins over a nested .git directory", cwd: filepath.Join(super, "lib"), recorded: []string{super}, want: super},
		{name: "the nearest recorded root wins", cwd: filepath.Join(super, "web", "src"), recorded: []string{super, filepath.Join(super, "web")}, want: filepath.Join(super, "web")},
		{name: "a recorded root is spelled as cwd spells it", cwd: filepath.Join(alias, "web"), recorded: []string{super}, want: alias},
		{name: "a recorded root that is gone is not used", cwd: filepath.Join(repo, "web"), recorded: []string{filepath.Join(home, "gone")}, want: repo},
		{name: "a recorded root elsewhere is not used", cwd: filepath.Join(super, "web"), recorded: []string{repo}, ambiguous: []string{filepath.Join(super, "web"), super}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := Roots{Home: home, Recorded: c.recorded}.Find(c.cwd)
			switch {
			case c.want != "":
				if got != c.want || err != nil {
					t.Fatalf("Find(%q) = %q, %v, want %q", c.cwd, got, err, c.want)
				}
			case c.ambiguous != nil:
				if got != "" || err == nil || !strings.Contains(err.Error(), "more than one directory") || !strings.Contains(err.Error(), strings.Join(c.ambiguous, " and ")) {
					t.Fatalf("Find(%q) = %q, %v, want an error that names %v", c.cwd, got, err, c.ambiguous)
				}
			default:
				if got != "" || err == nil || !strings.Contains(err.Error(), "no project root") {
					t.Fatalf("Find(%q) = %q, %v, want an error that says no project root", c.cwd, got, err)
				}
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
	env := Env{Root: Roots{Home: home}.Find}
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
	root := Roots{Home: home}.Find
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
