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
