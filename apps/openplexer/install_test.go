package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const userSettings = "{\n    \"model\": \"opus\",\n    \"hooks\": {\n        \"Stop\": [{\"hooks\": [{\"type\": \"command\", \"command\": \"say done\"}]}]\n    }\n}\n"

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(text)
}

func TestInstallAndUninstallKeepTheUserSettings(t *testing.T) {
	c := loadedCLI(t)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	settings := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "settings.json")
	writeFile(t, settings, userSettings)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	if out := c.must(exitOK, "install", "claude-code"); !strings.Contains(out, "installed the OpenPlexer hook of claude-code in "+settings) || strings.Contains(out, "no verified policy") {
		t.Errorf("install printed %q, want the file it changed and no warning", out)
	}
	installed := readFile(t, settings)
	if !strings.Contains(installed, `"command": "`+executable+` hook claude-code pre-tool"`) || !strings.Contains(installed, `"onFailure": "block"`) || !strings.Contains(installed, `"Stop": [{"hooks": [{"type": "command", "command": "say done"}]}],`) {
		t.Errorf("settings after install:\n%s\nwant the user's content and an entry that runs this binary", installed)
	}
	if out := c.must(exitOK, "install", "claude-code"); !strings.Contains(out, "already") || readFile(t, settings) != installed {
		t.Errorf("a second install printed %q and changed the file", out)
	}
	if out := c.must(exitOK, "uninstall", "claude-code"); !strings.Contains(out, "removed the OpenPlexer hook of claude-code from "+settings) {
		t.Errorf("uninstall printed %q", out)
	}
	if got := readFile(t, settings); got != userSettings {
		t.Errorf("settings after uninstall:\n%s\nwant the user's file, byte for byte:\n%s", got, userSettings)
	}
	if out := c.must(exitOK, "uninstall", "claude-code"); !strings.Contains(out, "nothing changed") {
		t.Errorf("a second uninstall printed %q", out)
	}
}

func TestInstallCodexInAProjectAndRemoveIt(t *testing.T) {
	c := newCLI(t)
	root := project(t)
	hooks := filepath.Join(root, ".codex", "hooks.json")
	out := c.must(exitOK, "install", "codex", "--scope", "project", "--project", root)
	if !strings.Contains(out, "in "+hooks) || !strings.Contains(out, "/hooks") || !strings.Contains(out, "no verified policy") {
		t.Errorf("install printed %q, want the file, the trust step of Codex, and a warning that no policy is loaded", out)
	}
	if text := readFile(t, hooks); !strings.Contains(text, `"matcher": "Bash|apply_patch"`) || strings.Contains(text, "onFailure") {
		t.Errorf("hooks.json after install:\n%s", text)
	}
	if out := c.must(exitOK, "uninstall", "codex", "--scope", "project", "--project", root); !strings.Contains(out, "deleted "+hooks) {
		t.Errorf("uninstall printed %q, want the file deleted", out)
	}
	if _, err := os.Stat(hooks); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("hooks.json is still there: %v", err)
	}
}

func TestInstallRefusesAFileThatItCannotParse(t *testing.T) {
	c := newCLI(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	const broken = "{\n  \"model\": \"opus\",\n}\n"
	writeFile(t, path, broken)
	for _, command := range []string{"install", "uninstall"} {
		if out := c.must(exitFailure, command, "claude-code", "--file", path); !strings.Contains(out, "invalid JSON at line 3") || !strings.Contains(out, "The file is unchanged") {
			t.Errorf("%s printed %q, want the place of the error and that the file is unchanged", command, out)
		}
		if readFile(t, path) != broken {
			t.Fatalf("%s changed a file that it refused", command)
		}
	}
}

func TestInstallUsage(t *testing.T) {
	c := newCLI(t)
	for _, args := range [][]string{
		{"install"},
		{"install", "gemini"},
		{"install", "codex", "--scope", "team"},
		{"install", "codex", "--project", "."},
		{"uninstall", "codex", "extra"},
		{"hook", "gemini", "pre-tool"},
	} {
		if out := c.must(exitUsage, args...); !strings.Contains(out, "usage:") {
			t.Errorf("openplexer %v printed %q, want the usage", args, out)
		}
	}
}

func TestUninstallKeepsAHookOfTheUserThatLooksLikeOurs(t *testing.T) {
	c := newCLI(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	const theirs = `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/opt/openplexer-lint hook claude-code --strict"}]}]}}`
	writeFile(t, path, theirs)
	if out := c.must(exitOK, "uninstall", "claude-code", "--file", path); !strings.Contains(out, "nothing changed") {
		t.Errorf("uninstall printed %q, want nothing changed", out)
	}
	if got := readFile(t, path); got != theirs {
		t.Errorf("the file after uninstall:\n%s\nwant the user's hook, byte for byte", got)
	}
}

func TestUninstallKeepsAFileThatInstallDidNotCreate(t *testing.T) {
	for name, before := range map[string]string{
		"only whitespace":          "  \n",
		"only a byte order mark":   "\xEF\xBB\xBF",
		"no bytes":                 "",
		"an empty object":          "{}",
		"an empty PreToolUse list": `{"hooks":{"PreToolUse":[]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := newCLI(t)
			path := filepath.Join(t.TempDir(), "settings.json")
			writeFile(t, path, before)
			c.must(exitOK, "install", "claude-code", "--file", path)
			c.must(exitOK, "uninstall", "claude-code", "--file", path)
			if got := readFile(t, path); got != before {
				t.Errorf("the file after install and uninstall is %q, want %q", got, before)
			}
		})
	}
}
