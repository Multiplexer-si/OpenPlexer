//go:build unix

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/testbin"
)

type portableCLI struct {
	bin string
}

func TestPortableHooks(t *testing.T) {
	bin := prebuiltCLI
	if bin == "" {
		bin = filepath.Join(t.TempDir(), "openplexer")
		build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build openplexer: %v\n%s", err, output)
		}
	}
	c := portableCLI{bin: bin}
	for _, agent := range []string{"claude-code", "codex"} {
		t.Run(agent, func(t *testing.T) {
			base := t.TempDir()
			user := mkdir(t, filepath.Join(base, "user"))
			otherUser := mkdir(t, filepath.Join(base, "other-user"))
			outer := mkdir(t, filepath.Join(base, "outer"))
			inner := mkdir(t, filepath.Join(outer, "inner"))
			sibling := mkdir(t, filepath.Join(base, "sibling"))
			outerHome := filepath.Join(sibling, "homes", "outer")
			innerHome := filepath.Join(base, "inner-home")
			siblingHome := filepath.Join(base, "sibling-home")
			c.git(t, user, outer, "init", "-q")
			c.commit(t, user, outer, "initial")
			worktree := filepath.Join(outer, "worktree")
			c.git(t, user, outer, "worktree", "add", "-q", "--detach", worktree)
			c.git(t, user, inner, "init", "-q")
			c.git(t, user, sibling, "init", "-q")
			c.policy(t, user, outer, outerHome, 1, []string{"app:**"}, nil)
			c.policy(t, user, inner, innerHome, 1, []string{"app:src/**"}, nil)
			c.policy(t, user, sibling, siblingHome, 1, []string{"app:src/**", "app:homes/**"}, []string{"touch"})
			c.cli(t, user, sibling, exitOK, "configure", "--home", siblingHome, "--project-root", ".")
			settings := portableSettings(agent)
			unrelated := `{"other":{"label":"keep","number":1e+03}}` + "\n"
			writeFile(t, filepath.Join(outer, settings), unrelated)
			c.cli(t, user, outer, exitOK, "--home", outerHome, "install", agent, "--project", ".", "--portable")
			command, contents := installedCommand(t, outer, agent)
			if command != "openplexer hook "+agent+" pre-tool --project-home" || !strings.Contains(contents, strings.TrimSuffix(unrelated[1:], "}\n")) {
				t.Fatalf("unexpected portable settings: %s", contents)
			}
			c.cli(t, user, outer, exitOK, "install", agent, "--project", ".", "--portable")
			if _, got := installedCommand(t, outer, agent); got != contents {
				t.Fatal("a second install changed the hook settings")
			}
			c.git(t, user, outer, "add", settings)
			c.commit(t, user, outer, "portable hook")
			check := func(name, input, want string) {
				t.Helper()
				t.Run(name, func(t *testing.T) { c.hook(t, user, outer, command, input, want) })
			}
			shell := func(cwd, line string) string { return callJSON("Bash", cwd, map[string]string{"command": line}) }
			check("unregistered nested repository uses outer policy", writeCall(agent, inner, "denied.txt"), "")
			c.cli(t, user, inner, exitOK, "configure", "--project-root", ".", "--home", innerHome)
			multi := shell(outer, "touch src/a inner/denied.txt")
			if agent == "codex" {
				multi = callJSON("apply_patch", outer, map[string]string{"command": "*** Begin Patch\n*** Add File: src/a\n+a\n*** Add File: inner/denied.txt\n+b\n*** End Patch"})
			}
			check("inner denial blocks both writes", multi, "denied a write to denied.txt")
			c.policy(t, user, inner, innerHome, 2, []string{"app:**"}, nil)
			check("both policies allow one call", multi, "")

			writeFile(t, filepath.Join(sibling, "denied.txt"), "file")
			symlink(t, filepath.Join(sibling, "denied.txt"), filepath.Join(outer, "link"))
			rename := shell(outer, "mv link renamed")
			unlink := shell(outer, "unlink link")
			write := writeCall(agent, outer, "link")
			check("rename a link", rename, "")
			check("unlink a link", unlink, "")
			if agent == "codex" {
				check("patch deletes a link entry", callJSON("apply_patch", outer, map[string]string{"command": "*** Begin Patch\n*** Delete File: link\n*** End Patch"}), "")
			}
			check("write through a link", write, "denied a write to denied.txt")
			mkdir(t, filepath.Join(sibling, "src"))
			symlink(t, sibling, filepath.Join(outer, "alias"))
			check("resolve existing parents of new files", writeCall(agent, outer, "alias/src/new/deep.txt"), "")
			check("resolve symlink before dot dot", writeCall(agent, outer, "alias/src/../denied.txt"), "denied a write to denied.txt")
			check("use actual rename destination in a directory", shell(outer, "mv link alias"), "denied a write to link")
			check("use actual rename destination in an allowed directory", shell(outer, "mv link alias/src"), "")
			check("BSD mv replaces the destination link entry", shell(outer, "mv -fh link alias"), "")
			check("GNU mv replaces the destination link entry", shell(outer, "mv --no-target-directory link alias"), "")
			check("BSD mv still moves into a real directory", shell(outer, "mv -h link alias/src"), "")
			check("git mv uses the destination link entry", shell(outer, "git mv -f link alias"), "")
			check("ln does not follow a directory link with -n", shell(outer, "ln -sfn link alias"), "")
			check("ln normally uses the destination directory", shell(outer, "ln -sf link alias"), "denied a write to link")
			symlink(t, filepath.Join(sibling, "denied.txt"), filepath.Join(outer, "copy-dest"))
			check("cp -P replaces a link when copying a link", shell(outer, "cp -P link copy-dest"), "")
			writeFile(t, filepath.Join(outer, "regular"), "file")
			check("cp -P follows a link when copying a regular file", shell(outer, "cp -P regular copy-dest"), "denied a write to denied.txt")
			check("GNU cp removes the link before writing", shell(outer, "cp --remove-destination regular copy-dest"), "")
			check("install replaces the link entry", shell(outer, "install regular copy-dest"), "")
			check("touch -h acts on the link entry", shell(outer, "touch -h link"), "")
			check("touch normally acts on the referent", shell(outer, "touch link"), "denied a write to denied.txt")
			guardedLink := ".claude/settings.local.json"
			mkdir(t, filepath.Join(outer, ".claude"))
			guardedReferent := mkdir(t, filepath.Join(sibling, "src", "settings-referent"))
			symlink(t, guardedReferent, filepath.Join(outer, guardedLink))
			check("guard the destination entry of BSD mv", shell(outer, "mv -h link "+guardedLink), "configures the hooks")
			check("guard the destination entry of BSD ln", shell(outer, "ln -sfh link "+guardedLink), "configures the hooks")
			const recursiveCopy = "cp -R examples backup"
			for _, line := range []string{"mv --unknown link alias", "cp -Z regular copy-dest", "touch --unknown regular", "git mv -h link alias", recursiveCopy} {
				out, errOut, status := c.run(t, user, outer, shell(outer, line), "/bin/sh", "-c", command)
				if status != 2 || out != "" || !strings.Contains(errOut, "unsupported option") {
					t.Fatalf("hook for %q returned exit %d, stdout %q and stderr %q, want an option refusal", line, status, out, errOut)
				}
			}
			check("use actual rename destination for git mv", shell(outer, "git mv link alias/src"), "")
			check("home under another checkout", writeCall(agent, outer, filepath.Join(outerHome, "keys.json")), "it holds the policy")
			check("move a directory containing a home", shell(outer, "mv alias/homes trash"), "it holds the policy")
			check("remove a directory containing a home", shell(outer, "rm -r alias/homes"), "it holds the policy")
			check("allow an unguarded write in the other checkout", writeCall(agent, outer, "alias/src/okay.txt"), "")
			check("settings guard precedes policy", writeCall(agent, outer, settings), "configures the hooks")
			check("guard the actual copy destination", shell(outer, "cp "+filepath.Base(settings)+" "+filepath.Dir(settings)), "configures the hooks")
			check("registry guard precedes lookup", writeCall(agent, outer, filepath.Join(user, ".openplexer", "projects.json")), "project registry")
			t.Run("case aliases use the same policy and guards", func(t *testing.T) {
				outerAlias := filepath.Join(base, "OUTER")
				if _, err := os.Stat(outerAlias); os.IsNotExist(err) {
					t.Skip("the test volume is case-sensitive")
				} else if err != nil {
					t.Fatal(err)
				}
				c.hook(t, user, outer, command, writeCall(agent, outerAlias, filepath.Join(outerAlias, "INNER", "new.txt")), "")
				c.hook(t, user, outer, command, writeCall(agent, outerAlias, filepath.Join(sibling, "SRC", "new.txt")), "")
				c.hook(t, user, outer, command, writeCall(agent, outerAlias, filepath.Join(sibling, "HOMES", "OUTER", "keys.json")), "it holds the policy")
				c.hook(t, user, outer, command, writeCall(agent, outerAlias, filepath.Join(user, ".OPENPLEXER", "PROJECTS.JSON")), "project registry")
				c.hook(t, user, outer, command, writeCall(agent, outerAlias, strings.ToUpper(settings)), "configures the hooks")
				c.hook(t, user, outer, command, writeCall(agent, outerAlias, ".CODEX/CONFIG.TOML"), "configures the hooks")
			})
			check("registry ancestor is guarded", shell(outer, "rm -r '"+filepath.Join(user, ".openplexer")+"'"), "project registry")
			check("all guards precede the first policy denial", shell(outer, "touch alias/denied.txt '"+filepath.Join(outerHome, "keys.json")+"'"), "it holds the policy")
			c.policy(t, user, outer, outerHome, 2, []string{"app:src/**", "app:worktree/**"}, nil)
			c.policy(t, user, sibling, siblingHome, 2, []string{"app:**"}, []string{"touch"})
			check("reverse policies for rename", rename, "denied a write to link")
			check("reverse policies for unlink", unlink, "denied a write to link")
			check("reverse policies for write through a link", write, "")
			check("exec uses the resolved cwd", shell(filepath.Join(outer, "alias"), "rm src/a"), "denied the command")
			check("exec uses the resolved cwd and allows touch", shell(filepath.Join(outer, "alias"), "touch src/a"), "")
			check("unregistered worktree uses outer policy", writeCall(agent, worktree, "deny.txt"), "")
			c.cli(t, user, worktree, exitOK, "--home", outerHome, "configure", "--project-root", ".")
			check("registered worktree has its own relative paths", writeCall(agent, worktree, "deny.txt"), "denied a write to deny.txt")
			out := c.cli(t, user, worktree, exitOK, "policy", "status")
			if !strings.Contains(out, "home: "+canonicalDir(t, outerHome)) {
				t.Fatalf("CLI status did not use the worktree registration: %s", out)
			}
			c.cli(t, user, worktree, exitFailure, "configure", "--home", innerHome, "--project-root", ".")
			check("sibling prefix is not an ancestor", writeCall(agent, outer, filepath.Join(mkdir(t, outer+"-other"), "file")), "configure --project-root")
			symlink(t, "loop", filepath.Join(outer, "loop"))
			check("unresolvable write target", writeCall(agent, outer, "loop"), "cannot resolve a write target")

			clone := filepath.Join(base, "clone")
			c.git(t, otherUser, base, "clone", "--local", "-q", outer, clone)
			cloneCommand, cloneContents := installedCommand(t, clone, agent)
			if cloneCommand != command || cloneContents != contents {
				t.Fatal("the clone did not inherit the committed settings")
			}
			c.hook(t, otherUser, clone, cloneCommand, writeCall(agent, clone, "src/a"), "cannot read the project registry")
			c.hook(t, user, clone, cloneCommand, writeCall(agent, clone, "src/a"), "configure --project-root")
			c.hook(t, otherUser, clone, cloneCommand, shell(clone, recursiveCopy), "cannot read the project registry")
			c.hook(t, user, clone, cloneCommand, shell(clone, recursiveCopy), "configure --project-root")
			cloneHome := filepath.Join(base, "clone-home")
			c.policy(t, otherUser, clone, cloneHome, 1, []string{"app:clone/**"}, nil)
			c.cli(t, otherUser, clone, exitOK, "configure", "--project-root", ".", "--home", cloneHome)
			c.hook(t, otherUser, clone, cloneCommand, writeCall(agent, clone, "clone/a"), "")
			c.hook(t, otherUser, clone, cloneCommand, writeCall(agent, clone, "src/a"), "denied a write to src/a")
			legacyHome := filepath.Join(otherUser, ".openplexer")
			c.policy(t, otherUser, clone, legacyHome, 1, []string{"app:legacy/**"}, nil)
			out, _, status := c.run(t, otherUser, clone, "", "/usr/bin/env", "OPENPLEXER_HOME="+legacyHome, c.bin, "policy", "status")
			if status != exitOK || !strings.Contains(out, "home: "+legacyHome) {
				t.Fatalf("OPENPLEXER_HOME did not take precedence over the registry: %s", out)
			}
			out, _, status = c.run(t, otherUser, clone, "", "/usr/bin/env", "OPENPLEXER_HOME="+legacyHome, c.bin, "--home", cloneHome, "policy", "status")
			if status != exitOK || !strings.Contains(out, "home: "+cloneHome) {
				t.Fatalf("--home did not take precedence over OPENPLEXER_HOME: %s", out)
			}
			c.hook(t, otherUser, clone, "OPENPLEXER_HOME='"+legacyHome+"' "+cloneCommand, writeCall(agent, clone, "legacy/a"), "denied a write to legacy/a")

			registry := filepath.Join(user, ".openplexer", "projects.json")
			prior, err := os.ReadFile(registry)
			if err != nil {
				t.Fatal(err)
			}
			bad := strings.TrimSpace(string(prior))
			bad = strings.TrimSuffix(bad, "}") + fmt.Sprintf(",%q:%q}", canonicalDir(t, inner), canonicalDir(t, outerHome))
			writeFile(t, registry, bad)
			check("duplicate root blocks affected checkout", writeCall(agent, inner, "src/a"), "conflicting homes")
			check("unaffected checkout still works", writeCall(agent, outer, "src/a"), "")
			writeFile(t, registry, string(prior))
			if err := os.Rename(innerHome, innerHome+"-moved"); err != nil {
				t.Fatal(err)
			}
			check("missing selected home", writeCall(agent, inner, "src/a"), "configure --project-root")
			check("missing shell home takes precedence over option refusal", shell(inner, recursiveCopy), "configure --project-root")

			c.cli(t, user, outer, exitOK, "install", agent, "--project", ".")
			legacyCommand, _ := installedCommand(t, outer, agent)
			if legacyCommand != c.bin+" hook "+agent+" pre-tool" {
				t.Fatalf("unexpected legacy command: %s", legacyCommand)
			}
			c.hook(t, otherUser, clone, legacyCommand, writeCall(agent, clone, "legacy/a"), "")
			c.hook(t, otherUser, clone, legacyCommand, writeCall(agent, clone, "clone/a"), "denied a write to clone/a")
			c.cli(t, user, outer, exitOK, "uninstall", agent, "--project", ".")
			if got, err := os.ReadFile(filepath.Join(outer, settings)); err != nil || string(got) != unrelated {
				t.Fatalf("uninstall did not preserve settings: %q, %v", got, err)
			}
		})
	}
}

func (c portableCLI) policy(t *testing.T, user, cwd, home string, version int, paths, execRules []string) {
	t.Helper()
	if execRules == nil {
		execRules = []string{}
	}
	keys, message := signedPolicy(t, map[string]any{
		"project_id": "prj-test", "version": version, "repos": []string{"app"},
		"areas":   []any{map[string]any{"name": "source", "patterns": paths}},
		"roles":   map[string]any{"dev": map[string]any{"read": []string{}, "write": []string{"source"}, "exec": execRules, "push": []string{}}},
		"members": map[string]string{"mem-dev": "dev"},
	})
	c.cli(t, user, cwd, exitOK, "--home", home, "configure", "--keys", keys, "--member", "mem-dev")
	c.cli(t, user, cwd, exitOK, "--home", home, "policy", "load", message)
}

func (c portableCLI) cli(t *testing.T, user, cwd string, want int, args ...string) string {
	t.Helper()
	out, stderr, status := c.run(t, user, cwd, "", c.bin, args...)
	if status != want {
		t.Fatalf("openplexer %s: exit %d, want %d\n%s%s", strings.Join(args, " "), status, want, out, stderr)
	}
	return out
}

func (c portableCLI) hook(t *testing.T, user, cwd, command, input, want string) {
	t.Helper()
	out, stderr, status := c.run(t, user, cwd, input, "/bin/sh", "-c", command)
	if status != 0 || stderr != "" || !strings.Contains(out, want) || (out == "") != (want == "") ||
		(out != "" && !strings.Contains(out, `"permissionDecision":"deny"`)) {
		t.Fatalf("hook exit %d, stdout %q, stderr %q, want deny reason %q or no output for allow", status, out, stderr, want)
	}
}

func (c portableCLI) git(t *testing.T, user, cwd string, args ...string) {
	t.Helper()
	out, stderr, status := c.run(t, user, cwd, "", "git", args...)
	if status != 0 {
		t.Fatalf("git %s: exit %d\n%s%s", strings.Join(args, " "), status, out, stderr)
	}
}

func (c portableCLI) commit(t *testing.T, user, cwd, message string) {
	t.Helper()
	c.git(t, user, cwd, "-c", "user.name=Hook Test", "-c", "user.email=hooks@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", message)
}

func (c portableCLI) run(t *testing.T, user, cwd, input, file string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), file, args...)
	cmd.Dir = cwd
	cmd.Env = []string{"HOME=" + user, "PATH=" + filepath.Dir(c.bin) + string(os.PathListSeparator) + os.Getenv("PATH")}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.Stdin = &stdout, &stderr, strings.NewReader(input)
	status, err := testbin.Run(cmd)
	if err != nil {
		t.Fatal(err)
	}
	return stdout.String(), stderr.String(), status
}

func writeCall(agent, cwd, path string) string {
	if agent == "codex" {
		return callJSON("apply_patch", cwd, map[string]string{"command": "*** Begin Patch\n*** Add File: " + path + "\n+x\n*** End Patch"})
	}
	return callJSON("Write", cwd, map[string]string{"file_path": path})
}

func portableSettings(agent string) string {
	if agent == "codex" {
		return ".codex/hooks.json"
	}
	return ".claude/settings.json"
}

func installedCommand(t *testing.T, dir, agent string) (string, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, portableSettings(agent)))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct{ Command string }
		}
	}
	readJSON(t, filepath.Join(dir, portableSettings(agent)), &settings)
	groups := settings.Hooks["PreToolUse"]
	if len(groups) != 1 || len(groups[0].Hooks) != 1 {
		t.Fatalf("expected one installed hook: %s", data)
	}
	return groups[0].Hooks[0].Command, string(data)
}

func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func symlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func canonicalDir(t *testing.T, path string) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
