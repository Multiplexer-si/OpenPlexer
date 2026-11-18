package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var nestedRepositories = map[string]func(t *testing.T, dir string){
	"a submodule with a gitlink file": func(t *testing.T, dir string) {
		writeFile(t, filepath.Join(dir, ".git"), "gitdir: ../.git/modules/web\n")
	},
	"a nested .git directory": func(t *testing.T, dir string) {
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	},
}

func TestHookKeepsTheRootOfAProjectInstallInsideANestedRepository(t *testing.T) {
	for name, nest := range nestedRepositories {
		t.Run(name, func(t *testing.T) {
			c := loadedCLI(t)
			root := project(t)
			web := filepath.Join(root, "web")
			c.must(exitOK, "install", "claude-code", "--scope", "project", "--project", root)
			c.must(exitOK, "install", "codex", "--scope", "project", "--project", root)
			nest(t, web)
			for _, step := range []struct{ name, agent, call, denied string }{
				{"a Write outside the nested repository", "claude-code", callJSON("Write", web, map[string]string{"file_path": filepath.Join(root, "api", "server.ts"), "content": "x"}), "denied a write to api/server.ts"},
				{"an rm that climbs out of it", "claude-code", callJSON("Bash", web, map[string]string{"command": "rm ../api/server.ts"}), "denied a write to api/server.ts"},
				{"a patch that deletes a file outside it", "codex", callJSON("apply_patch", web, map[string]string{"command": "*** Begin Patch\n*** Delete File: ../api/server.ts\n*** End Patch"}), "denied a write to api/server.ts"},
				{"a Write inside it", "claude-code", callJSON("Write", web, map[string]string{"file_path": filepath.Join(web, "src", "a.go"), "content": "x"}), "denied a write to web/src/a.go"},
				{"an Edit of the project settings", "claude-code", callJSON("Edit", web, map[string]string{"file_path": filepath.Join(root, ".claude", "settings.json"), "old_string": "a", "new_string": "b"}), "OpenPlexer blocked a write to"},
			} {
				status, reason, stderr := c.hookCall(step.agent, step.call)
				if status != 0 || stderr != "" || !strings.Contains(reason, step.denied) {
					t.Errorf("%s: exit %d, reason %q, stderr %q, want a denial that says %q", step.name, status, reason, stderr, step.denied)
				}
			}
		})
	}
}

func TestHookBlocksACallWhenTwoDirectoriesCouldBeTheRoot(t *testing.T) {
	for name, nest := range nestedRepositories {
		t.Run(name, func(t *testing.T) {
			c := loadedCLI(t)
			root := project(t)
			web := filepath.Join(root, "web")
			nest(t, web)
			for _, step := range []struct{ name, agent, call string }{
				{"a Write outside the nested repository", "claude-code", callJSON("Write", web, map[string]string{"file_path": filepath.Join(root, "api", "server.ts"), "content": "x"})},
				{"an rm that climbs out of it", "claude-code", callJSON("Bash", web, map[string]string{"command": "rm ../api/server.ts"})},
				{"a patch that deletes a file outside it", "codex", callJSON("apply_patch", web, map[string]string{"command": "*** Begin Patch\n*** Delete File: ../api/server.ts\n*** End Patch"})},
				{"a Write inside it", "claude-code", callJSON("Write", web, map[string]string{"file_path": filepath.Join(web, "src", "a.go"), "content": "x"})},
				{"an Edit of the project settings", "claude-code", callJSON("Edit", web, map[string]string{"file_path": filepath.Join(root, ".claude", "settings.json"), "old_string": "a", "new_string": "b"})},
			} {
				status, reason, _ := c.hookCall(step.agent, step.call)
				if status != 0 || !strings.Contains(reason, root) || !strings.Contains(reason, web) || !strings.Contains(reason, "openplexer install "+step.agent+" --project") {
					t.Errorf("%s: exit %d, reason %q, want a denial that names both directories and how to record the root", step.name, status, reason)
				}
			}
			inRoot := callJSON("Write", root, map[string]string{"file_path": "src/a.go", "content": "x"})
			if status, reason, _ := c.hookCall("claude-code", inRoot); status != 0 || reason != "" {
				t.Errorf("a write from the outer directory: exit %d, reason %q, want it judged and allowed", status, reason)
			}
		})
	}
}

func TestProjectFlagMeansTheProjectScopeAndUninstallForgetsTheRoot(t *testing.T) {
	c := loadedCLI(t)
	root := project(t)
	web := filepath.Join(root, "web")
	nestedRepositories["a nested .git directory"](t, web)
	call := callJSON("Write", web, map[string]string{"file_path": filepath.Join(root, "src", "a.go"), "content": "x"})

	out := c.must(exitOK, "install", "claude-code", "--project", root)
	if !strings.Contains(out, "in "+filepath.Join(root, ".claude", "settings.json")) || !strings.Contains(out, "recorded "+root+" as the project root") {
		t.Errorf("install --project printed %q, want the project hook file and the recorded root", out)
	}
	if status, reason, _ := c.hookCall("claude-code", call); status != 0 || reason != "" {
		t.Errorf("with the root recorded: exit %d, reason %q, want the write to src judged and allowed", status, reason)
	}
	if status, reason, _ := c.hookCall("codex", callJSON("Bash", web, map[string]string{"command": "touch ../src/b.go"})); status != 0 || !strings.Contains(reason, `denied the command "touch ../src/b.go"`) {
		t.Errorf("the other agent in the recorded project: exit %d, reason %q, want the call judged from the recorded root", status, reason)
	}
	if out := c.must(exitOK, "install", "claude-code", "--project", root); !strings.Contains(out, "already") {
		t.Errorf("a second install printed %q", out)
	}
	c.must(exitOK, "uninstall", "claude-code", "--project", root)
	if entries, _ := os.ReadDir(filepath.Join(os.Getenv("OPENPLEXER_HOME"), "installs")); len(entries) != 0 {
		t.Errorf("the journal still holds %d entries after uninstall", len(entries))
	}
	if status, reason, _ := c.hookCall("claude-code", call); status != 0 || !strings.Contains(reason, "more than one directory") {
		t.Errorf("after uninstall: exit %d, reason %q, want the two candidates named again", status, reason)
	}
}

func TestHookBlocksACallWhenItCannotReadTheJournal(t *testing.T) {
	c := loadedCLI(t)
	root := project(t)
	c.must(exitOK, "install", "codex", "--project", root)
	writeFile(t, filepath.Join(os.Getenv("OPENPLEXER_HOME"), "installs", "broken.json"), "{")
	call := callJSON("Write", root, map[string]string{"file_path": "src/a.go", "content": "x"})
	if status, reason, _ := c.hookCall("claude-code", call); status != 0 || !strings.Contains(reason, "cannot read its record of the installed projects") {
		t.Errorf("exit %d, reason %q, want a denial that names the record", status, reason)
	}
}
