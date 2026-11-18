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

func TestHookJudgesANestedRepositoryFromTheOutermostRepositoryWithNoRecord(t *testing.T) {
	for name, nest := range nestedRepositories {
		t.Run(name, func(t *testing.T) {
			c := loadedCLI(t)
			root := project(t)
			web := filepath.Join(root, "web")
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

func TestHookJudgesALinkedWorktreeInsideItsRepositoryFromTheWorktree(t *testing.T) {
	c := loadedCLI(t)
	root := project(t)
	worktree := filepath.Join(root, ".claude", "worktrees", "x")
	admin := filepath.Join(root, ".git", "worktrees", "x")
	writeFile(t, filepath.Join(worktree, ".git"), "gitdir: "+admin+"\n")
	writeFile(t, filepath.Join(admin, "commondir"), "../..\n")
	writeFile(t, filepath.Join(admin, "gitdir"), filepath.Join(worktree, ".git")+"\n")
	src := filepath.Join(worktree, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ name, agent, call, denied string }{
		{"a Write inside the worktree", "claude-code", callJSON("Write", src, map[string]string{"file_path": filepath.Join(worktree, "api", "server.ts"), "content": "x"}), "denied a write to api/server.ts"},
		{"an rm that climbs out of cwd", "claude-code", callJSON("Bash", src, map[string]string{"command": "rm ../api/server.ts"}), "denied a write to api/server.ts"},
		{"a patch that deletes a file of the worktree", "codex", callJSON("apply_patch", src, map[string]string{"command": "*** Begin Patch\n*** Delete File: ../api/server.ts\n*** End Patch"}), "denied a write to api/server.ts"},
		{"an Edit of the settings of the worktree", "claude-code", callJSON("Edit", src, map[string]string{"file_path": filepath.Join(worktree, ".claude", "settings.json"), "old_string": "a", "new_string": "b"}), "OpenPlexer blocked a write to"},
		{"a Write into the main checkout", "claude-code", callJSON("Write", src, map[string]string{"file_path": filepath.Join(root, "api", "server.ts"), "content": "x"}), "denied a write to api/server.ts"},
		{"an rm that climbs into the main checkout", "codex", callJSON("Bash", src, map[string]string{"command": "rm ../../../../api/server.ts"}), "denied a write to api/server.ts"},
		{"an Edit of the settings of the main checkout", "claude-code", callJSON("Edit", src, map[string]string{"file_path": filepath.Join(root, ".claude", "settings.json"), "old_string": "a", "new_string": "b"}), "OpenPlexer blocked a write to"},
	} {
		status, reason, stderr := c.hookCall(step.agent, step.call)
		if status != 0 || stderr != "" || !strings.Contains(reason, step.denied) {
			t.Errorf("%s: exit %d, reason %q, stderr %q, want a denial that says %q", step.name, status, reason, stderr, step.denied)
		}
	}
	inside := callJSON("Write", src, map[string]string{"file_path": "a.go", "content": "x"})
	if status, reason, _ := c.hookCall("claude-code", inside); status != 0 || reason != "" {
		t.Errorf("a write to src/a.go of the worktree: exit %d, reason %q, want it judged as src/a.go and allowed", status, reason)
	}

	c.must(exitOK, "install", "claude-code", "--project", root)
	if status, reason, _ := c.hookCall("claude-code", inside); status != 0 || !strings.Contains(reason, "denied a write to .claude/worktrees/x/src/a.go") {
		t.Errorf("with the repository recorded: exit %d, reason %q, want the write judged from the recorded root", status, reason)
	}
}

func TestHookJudgesAWriteFromAnInnerRecordedProjectByTheOuterOne(t *testing.T) {
	c := loadedCLI(t)
	root := project(t)
	web := filepath.Join(root, "web")
	nestedRepositories["a nested .git directory"](t, web)
	c.must(exitOK, "install", "claude-code", "--project", root)
	c.must(exitOK, "install", "claude-code", "--project", web)
	for _, step := range []struct{ name, call, denied string }{
		{"a Write into the outer project", callJSON("Write", web, map[string]string{"file_path": filepath.Join(root, "api", "server.ts"), "content": "x"}), "denied a write to api/server.ts"},
		{"an rm that climbs into the outer project", callJSON("Bash", web, map[string]string{"command": "rm ../api/server.ts"}), "denied a write to api/server.ts"},
		{"an Edit of the settings of the outer project", callJSON("Edit", web, map[string]string{"file_path": filepath.Join(root, ".claude", "settings.json"), "old_string": "a", "new_string": "b"}), "OpenPlexer blocked a write to"},
		{"a Write inside the inner project", callJSON("Write", web, map[string]string{"file_path": "api/server.ts", "content": "x"}), "denied a write to api/server.ts"},
	} {
		status, reason, stderr := c.hookCall("claude-code", step.call)
		if status != 0 || stderr != "" || !strings.Contains(reason, step.denied) {
			t.Errorf("%s: exit %d, reason %q, stderr %q, want a denial that says %q", step.name, status, reason, stderr, step.denied)
		}
	}
	if status, reason, _ := c.hookCall("claude-code", callJSON("Write", web, map[string]string{"file_path": "src/a.go", "content": "x"})); status != 0 || reason != "" {
		t.Errorf("a write to src/a.go of the inner project: exit %d, reason %q, want it judged from the inner root and allowed", status, reason)
	}
}

func TestHookBlocksACallWhenAnAgentDirectoryLiesAboveTheRepository(t *testing.T) {
	c := loadedCLI(t)
	outer := filepath.Join(t.TempDir(), "workspace")
	repo := filepath.Join(outer, "web")
	for _, dir := range []string{filepath.Join(outer, ".claude"), filepath.Join(repo, ".git")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, step := range []struct{ name, agent, call string }{
		{"a Write outside the repository", "claude-code", callJSON("Write", repo, map[string]string{"file_path": filepath.Join(outer, "api", "server.ts"), "content": "x"})},
		{"an rm that climbs out of it", "codex", callJSON("Bash", repo, map[string]string{"command": "rm ../api/server.ts"})},
		{"a Write inside it", "claude-code", callJSON("Write", repo, map[string]string{"file_path": "src/a.go", "content": "x"})},
	} {
		status, reason, _ := c.hookCall(step.agent, step.call)
		if status != 0 || !strings.Contains(reason, repo+" and "+outer+".") || !strings.Contains(reason, "openplexer install "+step.agent+" --project") {
			t.Errorf("%s: exit %d, reason %q, want a denial that names both directories and how to record the root", step.name, status, reason)
		}
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
	if status, reason, _ := c.hookCall("claude-code", call); status != 0 || reason != "" {
		t.Errorf("after uninstall: exit %d, reason %q, want the write to src judged from the outer repository and allowed", status, reason)
	}
}

func TestHookBlocksACallWhenItCannotReadTheJournal(t *testing.T) {
	c := loadedCLI(t)
	root := project(t)
	c.must(exitOK, "install", "codex", "--project", root)
	writeFile(t, filepath.Join(os.Getenv("OPENPLEXER_HOME"), "installs", "broken.json"), "{")
	call := callJSON("Write", root, map[string]string{"file_path": "src/a.go", "content": "x"})
	if status, reason, _ := c.hookCall("claude-code", call); status != 0 || !strings.Contains(reason, "the record of the installed projects cannot be read") {
		t.Errorf("exit %d, reason %q, want a denial that names the record", status, reason)
	}
}

func TestInstallAgainReplacesADamagedRecordOfTheSameHookFile(t *testing.T) {
	c := loadedCLI(t)
	root := project(t)
	c.must(exitOK, "install", "claude-code", "--project", root)
	installs := filepath.Join(os.Getenv("OPENPLEXER_HOME"), "installs")
	entries, err := os.ReadDir(installs)
	if err != nil || len(entries) != 1 {
		t.Fatalf("the journal holds %d entries (%v), want the one record", len(entries), err)
	}
	damaged := filepath.Join(installs, entries[0].Name())
	writeFile(t, damaged, `{"agent": "claude-co`)
	call := callJSON("Write", root, map[string]string{"file_path": "src/a.go", "content": "x"})
	if status, reason, _ := c.hookCall("claude-code", call); status != 0 || !strings.Contains(reason, damaged) || !strings.Contains(reason, "delete it yourself") {
		t.Errorf("with a damaged record: exit %d, reason %q, want a denial that names the file and says to delete it", status, reason)
	}
	c.must(exitOK, "install", "claude-code", "--project", root)
	if status, reason, _ := c.hookCall("claude-code", call); status != 0 || reason != "" {
		t.Errorf("after the second install: exit %d, reason %q, want the write judged and allowed", status, reason)
	}
	web := filepath.Join(root, "web")
	nestedRepositories["a nested .git directory"](t, web)
	climbs := callJSON("Bash", web, map[string]string{"command": "rm ../api/server.ts"})
	if _, reason, _ := c.hookCall("claude-code", climbs); !strings.Contains(reason, "denied a write to api/server.ts") {
		t.Errorf("after the second install: reason %q, want the recorded root back", reason)
	}
}
