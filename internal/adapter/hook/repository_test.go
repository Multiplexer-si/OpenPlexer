package hook

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type checkouts struct {
	home, bare, super string
	web, vendored     string
	scratch, inRepo   string
	external          string
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "protocol.file.allow=always", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func commitFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		path := filepath.Join(dir, name)
		mkdirs(t, filepath.Dir(path))
		if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "files")
}

func realCheckouts(t *testing.T) checkouts {
	t.Helper()
	home := t.TempDir()
	c := checkouts{
		home:     home,
		bare:     filepath.Join(home, "lib.git"),
		super:    filepath.Join(home, "super"),
		external: filepath.Join(home, "external"),
	}
	c.web = filepath.Join(c.super, "web")
	c.vendored = filepath.Join(c.super, "vendored")
	c.scratch = filepath.Join(c.super, "scratch")
	c.inRepo = filepath.Join(c.super, ".claude", "worktrees", "x")

	seed := filepath.Join(home, "seed")
	mkdirs(t, seed, c.super)
	git(t, seed, "init", "-q")
	commitFiles(t, seed, "src/a.go")
	git(t, home, "clone", "-q", "--bare", seed, c.bare)

	git(t, c.super, "init", "-q")
	commitFiles(t, c.super, "api/server.ts", "src/a.go", ".claude/settings.json")
	git(t, c.super, "submodule", "--quiet", "add", c.bare, "web")
	git(t, c.super, "clone", "-q", c.bare, "vendored")
	git(t, c.super, "submodule", "--quiet", "add", c.bare, "vendored")
	git(t, c.super, "commit", "-q", "-m", "submodules")

	git(t, c.super, "worktree", "add", "-q", c.inRepo, "-b", "x")
	git(t, c.inRepo, "submodule", "--quiet", "update", "--init", "web")
	git(t, c.super, "-c", "worktree.useRelativePaths=true", "worktree", "add", "-q", filepath.Join(c.super, ".claude", "worktrees", "relative"), "-b", "relative")
	git(t, c.super, "worktree", "add", "-q", filepath.Join(c.super, "before-move"), "-b", "moved")
	if err := os.Rename(filepath.Join(c.super, "before-move"), filepath.Join(c.super, "moved")); err != nil {
		t.Fatal(err)
	}
	git(t, c.web, "worktree", "add", "-q", filepath.Join(c.super, "web-worktree"), "-b", "w")

	git(t, c.super, "init", "-q", "scratch")
	git(t, c.scratch, "commit", "-q", "--allow-empty", "-m", "empty")
	git(t, c.scratch, "worktree", "add", "-q", filepath.Join(c.scratch, "worktree"), "-b", "s")
	mkdirs(t, filepath.Join(c.super, "forged"))
	forged := "gitdir: " + filepath.Join(c.super, ".git", "worktrees", "x") + "\n"
	if err := os.WriteFile(filepath.Join(c.super, "forged", ".git"), []byte(forged), 0o644); err != nil {
		t.Fatal(err)
	}

	git(t, c.super, "worktree", "add", "-q", c.external, "-b", "external")
	git(t, c.external, "worktree", "add", "-q", filepath.Join(c.external, ".claude", "worktrees", "y"), "-b", "y")
	git(t, c.bare, "worktree", "add", "-q", filepath.Join(home, "of-bare"), "-b", "of-bare")
	git(t, home, "init", "-q", "--separate-git-dir", filepath.Join(home, "separate.git"), filepath.Join(home, "separate"))
	return c
}

func TestRootsFindReadsTheLayoutThatGitWrites(t *testing.T) {
	c := realCheckouts(t)
	sub := func(dir string, names ...string) string {
		path := filepath.Join(append([]string{dir}, names...)...)
		mkdirs(t, path)
		return path
	}
	t.Setenv("GIT_DIR", filepath.Join(c.scratch, ".git"))
	t.Setenv("GIT_WORK_TREE", c.scratch)

	for _, k := range []struct {
		name, cwd string
		recorded  []string
		want      string
	}{
		{name: "the superproject", cwd: sub(c.super, "api"), want: c.super},
		{name: "a submodule with a gitlink file", cwd: sub(c.web, "src"), want: c.super},
		{name: "a submodule with a .git directory", cwd: sub(c.vendored, "src"), want: c.super},
		{name: "git init in a subfolder", cwd: c.scratch, want: c.super},
		{name: "a linked worktree inside its repository", cwd: sub(c.inRepo, "src"), want: c.inRepo},
		{name: "a linked worktree that git wrote with relative paths", cwd: filepath.Join(c.super, ".claude", "worktrees", "relative"), want: filepath.Join(c.super, ".claude", "worktrees", "relative")},
		{name: "a submodule inside a linked worktree", cwd: sub(c.inRepo, "web", "src"), want: c.inRepo},
		{name: "a linked worktree outside its repository", cwd: sub(c.external, "src"), want: c.external},
		{name: "a linked worktree inside a linked worktree", cwd: filepath.Join(c.external, ".claude", "worktrees", "y"), want: filepath.Join(c.external, ".claude", "worktrees", "y")},
		{name: "a linked worktree of a bare repository", cwd: filepath.Join(c.home, "of-bare"), want: filepath.Join(c.home, "of-bare")},
		{name: "a repository with a separate git directory", cwd: filepath.Join(c.home, "separate"), want: filepath.Join(c.home, "separate")},
		{name: "a worktree of a submodule", cwd: filepath.Join(c.super, "web-worktree"), want: c.super},
		{name: "a worktree of a repository that git init made in a subfolder", cwd: filepath.Join(c.scratch, "worktree"), want: c.super},
		{name: "a .git file that names the worktree record of another directory", cwd: filepath.Join(c.super, "forged"), want: c.super},
		{name: "a linked worktree that was moved by hand", cwd: filepath.Join(c.super, "moved"), want: c.super},
		{name: "a bare repository", cwd: c.bare},
		{name: "a recorded superproject wins over a linked worktree", cwd: sub(c.inRepo, "src"), recorded: []string{c.super}, want: c.super},
		{name: "a recorded submodule wins over the superproject", cwd: sub(c.web, "src"), recorded: []string{c.web}, want: c.web},
		{name: "a recorded subfolder repository wins over the superproject", cwd: c.scratch, recorded: []string{c.scratch}, want: c.scratch},
	} {
		t.Run(k.name, func(t *testing.T) {
			got, err := Roots{Home: c.home, Recorded: k.recorded}.Find(k.cwd)
			if k.want == "" {
				if got != "" || err == nil || !strings.Contains(err.Error(), "no project root") {
					t.Fatalf("Find(%q) = %q, %v, want an error that says no project root", k.cwd, got, err)
				}
				return
			}
			if got != k.want || err != nil {
				t.Fatalf("Find(%q) = %q, %v, want %q", k.cwd, got, err, k.want)
			}
		})
	}
}

func TestCallsInANestedCheckoutAreJudgedFromTheRootOfItsRepository(t *testing.T) {
	c := realCheckouts(t)
	tools := Tools{"Write": FileTool("file_path"), "Edit": FileTool("file_path"), "Bash": ShellTool}
	guards := []Guard{{Path: ".claude/settings.json", Why: "it holds the hook of OpenPlexer"}}
	const guarded, unjudged = "guarded", ""
	type call struct{ tool, target, want string }
	fromTheSuperproject := func(nested string) []call {
		return []call{
			{"Write", filepath.Join(c.super, "api", "server.ts"), "write api/server.ts"},
			{"Bash", "rm ../api/server.ts", "write api/server.ts"},
			{"Write", filepath.Join(c.super, nested, "src", "a.go"), "write " + nested + "/src/a.go"},
			{"Write", "src/a.go", "write " + nested + "/src/a.go"},
			{"Edit", filepath.Join(c.super, ".claude", "settings.json"), guarded},
		}
	}
	inWorktree := filepath.Join(".claude", "worktrees", "x")

	for _, k := range []struct {
		name, cwd string
		recorded  []string
		calls     []call
	}{
		{name: "a submodule with a gitlink file", cwd: c.web, calls: fromTheSuperproject("web")},
		{name: "a submodule with a .git directory", cwd: c.vendored, calls: fromTheSuperproject("vendored")},
		{name: "git init in a subfolder", cwd: c.scratch, calls: fromTheSuperproject("scratch")},
		{name: "a linked worktree inside its repository", cwd: filepath.Join(c.inRepo, "src"), calls: []call{
			{"Write", filepath.Join(c.inRepo, "src", "a.go"), "write src/a.go"},
			{"Write", "a.go", "write src/a.go"},
			{"Bash", "rm ../api/server.ts", "write api/server.ts"},
			{"Edit", filepath.Join(c.inRepo, ".claude", "settings.json"), guarded},
			{"Write", filepath.Join(c.super, "api", "server.ts"), "write api/server.ts"},
			{"Bash", "rm ../../../../api/server.ts", "write api/server.ts"},
			{"Write", "../../../../src/a.go", "write src/a.go"},
			{"Edit", filepath.Join(c.super, ".claude", "settings.json"), guarded},
			{"Bash", "rm ../../../../.claude/settings.json", guarded},
			{"Write", filepath.Join(c.external, "src", "a.go"), "write src/a.go"},
			{"Write", filepath.Join(c.home, "loose", "a.go"), unjudged},
		}},
		{name: "the main checkout of a linked worktree", cwd: c.super, calls: []call{
			{"Write", filepath.Join(c.inRepo, "src", "a.go"), "write src/a.go"},
			{"Edit", filepath.Join(c.inRepo, ".claude", "settings.json"), guarded},
		}},
		{name: "a recorded submodule inside a recorded superproject", cwd: c.web, recorded: []string{c.super, c.web}, calls: []call{
			{"Write", "src/a.go", "write src/a.go"},
			{"Write", filepath.Join(c.super, "api", "server.ts"), "write api/server.ts"},
			{"Bash", "rm ../api/server.ts", "write api/server.ts"},
			{"Edit", filepath.Join(c.super, ".claude", "settings.json"), guarded},
		}},
		{name: "a recorded subfolder repository in a superproject with no record", cwd: c.scratch, recorded: []string{c.scratch}, calls: []call{
			{"Write", "src/a.go", "write src/a.go"},
			{"Bash", "rm ../api/server.ts", "write api/server.ts"},
		}},
		{name: "a linked worktree whose repository is recorded", cwd: filepath.Join(c.inRepo, "src"), recorded: []string{c.super}, calls: []call{
			{"Write", "a.go", "write " + filepath.ToSlash(inWorktree) + "/src/a.go"},
			{"Write", filepath.Join(c.super, "api", "server.ts"), "write api/server.ts"},
			{"Edit", filepath.Join(c.super, ".claude", "settings.json"), guarded},
		}},
	} {
		t.Run(k.name, func(t *testing.T) {
			env := Env{Root: Roots{Home: c.home, Recorded: k.recorded}.Find, Guards: guards}
			for _, call := range k.calls {
				field := "file_path"
				if call.tool == "Bash" {
					field = "command"
				}
				input := `{"hook_event_name":"PreToolUse","tool_name":"` + call.tool + `","cwd":"` + k.cwd + `","tool_input":{"` + field + `":"` + call.target + `"}}`
				var asked []string
				v := Decide([]byte(input), env, recordQueries(&asked), tools)
				switch call.want {
				case guarded:
					if v.Outcome != Deny || len(asked) != 0 || !strings.Contains(v.Reason, "hook of OpenPlexer") {
						t.Errorf("%s %s: verdict %+v after queries %q, want the guard to deny it", call.tool, call.target, v, asked)
					}
				case unjudged:
					if v.Outcome != Allow || len(asked) != 0 {
						t.Errorf("%s %s: verdict %+v after queries %q, want no query, because the target is outside the project", call.tool, call.target, v, asked)
					}
				default:
					if v.Outcome != Deny || len(asked) != 1 || asked[0] != call.want {
						t.Errorf("%s %s: verdict %+v after queries %q, want a denial after %q", call.tool, call.target, v, asked, call.want)
					}
				}
			}
		})
	}
}
