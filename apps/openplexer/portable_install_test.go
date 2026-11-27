//go:build unix

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
