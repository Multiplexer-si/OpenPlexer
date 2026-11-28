//go:build unix

package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortableReinstallPreservesProjectHome(t *testing.T) {
	for _, agent := range []string{"claude-code", "codex"} {
		t.Run(agent, func(t *testing.T) {
			c := newCLI(t)
			t.Setenv("HOME", t.TempDir())
			root := project(t)
			c.must(exitOK, "configure", "--member", "mem-dev")
			c.must(exitOK, "install", agent, "--project", root, "--portable")
			path := filepath.Join(root, portableSettings(agent))
			var before struct {
				Hooks map[string]json.RawMessage `json:"hooks"`
			}
			readJSON(t, path, &before)
			delete(before.Hooks, "SessionStart")
			old, err := json.Marshal(before)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, path, string(old))

			c.must(exitOK, "install", agent, "--project", root)
			_, got := installedCommand(t, root, agent)
			for _, stage := range []string{"pre-tool", "session-start"} {
				if want := "openplexer hook " + agent + " " + stage + ` --project-home"`; strings.Count(got, `"`+want) != 1 {
					t.Errorf("upgrade did not install one portable %s handler: %s", stage, got)
				}
			}
			c.must(exitOK, "install", agent, "--project", root)
			if _, repeated := installedCommand(t, root, agent); repeated != got {
				t.Error("reinstall changed the upgraded settings")
			}
			c.must(exitOK, "uninstall", agent, "--project", root)
			if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("uninstall left the upgraded hook file: %v", err)
			}
			c.must(exitOK, "install", agent, "--project", root, "--portable")
			c.must(exitOK, "install", agent, "--project", root, "--portable=false")
			command, converted := installedCommand(t, root, agent)
			if !filepath.IsAbs(command) || strings.Contains(converted, "--project-home") {
				t.Errorf("explicit conversion did not install local handlers: %s", converted)
			}
		})
	}
}

func TestPortableInstallLeavesRegistryAndMalformedSettingsUnchanged(t *testing.T) {
	for _, name := range []string{"missing registry", "existing registry"} {
		t.Run(name, func(t *testing.T) {
			c := newCLI(t)
			user := t.TempDir()
			t.Setenv("HOME", user)
			home := os.Getenv("OPENPLEXER_HOME")
			if err := os.MkdirAll(home, 0o700); err != nil {
				t.Fatal(err)
			}
			registry := filepath.Join(user, ".openplexer", "projects.json")
			const before = "{ }\n"
			if name == "existing registry" {
				if err := os.MkdirAll(filepath.Dir(registry), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(registry, []byte(before), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			root := project(t)
			settings := filepath.Join(root, ".claude", "settings.json")
			const malformed = "{\n  \"model\": \"opus\",\n}\n"
			writeFile(t, settings, malformed)
			out := c.must(exitFailure, "install", "claude-code", "--project", root, "--portable")
			if !strings.Contains(out, "invalid JSON at line 3") {
				t.Errorf("expected a settings validation error, got %q", out)
			}
			if got := readFile(t, settings); got != malformed {
				t.Error("portable install modified the malformed settings")
			}
			if name == "existing registry" {
				if got := readFile(t, registry); got != before {
					t.Error("portable install modified the registry")
				}
			} else if _, err := os.Stat(registry); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("portable install left a registry behind, stat error %v", err)
			}
		})
	}
}
