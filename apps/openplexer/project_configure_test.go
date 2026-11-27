//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureRegistrationFailurePreservesHomeAndRegistry(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
	}{
		{"missing checkout", "cannot register the checkout"},
		{"conflicting claim", "already registered with another home"},
		{"malformed registry", "must be a JSON object"},
		{"invalid settings", "must not be negative"},
		{"invalid home keys", "keys"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCLI(t)
			user := t.TempDir()
			t.Setenv("HOME", user)
			home := os.Getenv("OPENPLEXER_HOME")
			registered := t.TempDir()
			root := t.TempDir()
			c.must(exitOK, "configure", "--member", "mem-lee")
			c.must(exitOK, "configure", "--project-root", registered)
			registry := filepath.Join(user, ".openplexer", "projects.json")
			args := []string{"configure", "--member", "mem-ana"}
			switch tc.name {
			case "missing checkout":
				root = filepath.Join(root, "missing")
			case "conflicting claim":
				other := filepath.Join(t.TempDir(), "home")
				c.must(exitOK, "--home", other, "configure", "--member", "mem-other")
				c.must(exitOK, "--home", other, "configure", "--project-root", root)
			case "malformed registry":
				writeFile(t, registry, "[]\n")
			case "invalid settings":
				args = append(args, "--stale-after-days", "-1")
			case "invalid home keys":
				writeFile(t, filepath.Join(home, "keys.json"), "{}\n")
			}
			config := filepath.Join(home, "config.json")
			beforeConfig, beforeRegistry := readFile(t, config), readFile(t, registry)
			if out := c.must(exitFailure, append(args, "--project-root", root)...); !strings.Contains(out, tc.reason) {
				t.Error("configure did not report the expected failure")
			}
			if readFile(t, config) != beforeConfig {
				t.Error("failed configure changed config.json")
			}
			if readFile(t, registry) != beforeRegistry {
				t.Error("failed configure changed projects.json")
			}
		})
	}
}

func TestConfigureRegistersNewHomeWithSettings(t *testing.T) {
	c := newCLI(t)
	user := t.TempDir()
	t.Setenv("HOME", user)
	root := t.TempDir()
	home := os.Getenv("OPENPLEXER_HOME")
	c.must(exitOK, "configure", "--member", "mem-lee", "--project-root", root)
	var registry map[string]string
	readJSON(t, filepath.Join(user, ".openplexer", "projects.json"), &registry)
	if registry[canonicalDir(t, root)] != canonicalDir(t, home) {
		t.Fatal("configure did not register the new home")
	}
	if !strings.Contains(readFile(t, filepath.Join(home, "config.json")), "mem-lee") {
		t.Fatal("configure did not save the new member")
	}
}
