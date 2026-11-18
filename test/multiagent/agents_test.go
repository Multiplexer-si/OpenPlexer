package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/multiplexer-si/openplexer/test/internal/agentcli"
)

func TestAgentEnvironmentUsesRealHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USER", "tester")
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "other-claude-home")
	t.Setenv("CODEX_HOME", "other-codex-home")
	t.Setenv("UNRELATED_ENV", "do not inherit")
	r := run{dir: t.TempDir()}
	for _, name := range []string{"claude", "codex"} {
		p := r.newParticipant(name, name, "model", name, "")
		if err := p.prepare(); err != nil {
			t.Fatal(err)
		}
		want := []string{"HOME=" + home, "USER=tester", "PATH=/usr/bin:/bin", "SHELL=/bin/sh",
			"TMPDIR=" + os.Getenv("TMPDIR"), "OPENPLEXER_HOME=" + p.home}
		if name == "claude" {
			want = append(want, "CLAUDE_CODE_TMPDIR="+p.path("state"), "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1")
		}
		if !slices.Equal(p.env, want) {
			t.Fatalf("%s environment = %q, want %q", name, p.env, want)
		}
	}
}

func TestClaudeSessionPathsAreGuardedAndCleaned(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := run{dir: dir, session: agentcli.NewSessionID()}
	stub := filepath.Join(r.dir, "claude")
	const script = `#!/bin/sh
set -eu
slug=$(printf '%s' "$PWD" | tr -c 'A-Za-z0-9' '-')
mkdir -p "$HOME/.claude/projects/$slug" "$HOME/.claude/session-env/$3"
exit 3
`
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	p := r.newParticipant("claude", "claude-code", "haiku", "dev-a", stub)
	if err := p.prepare(); err != nil {
		t.Fatal(err)
	}
	leftovers := agentcli.ClaudeLeftovers(filepath.Join(home, ".claude"), p.project, r.session)
	for _, path := range leftovers {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		sibling := filepath.Join(filepath.Dir(path), "existing")
		writeFake(t, sibling, []byte("keep"))
		err := r.runAgent(t.Context(), p, "claim", "test")
		if err == nil || !strings.Contains(err.Error(), path+" already exists") {
			t.Fatalf("run with existing path %s: %v", path, err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	err = r.runAgent(t.Context(), p, "claim", "test")
	if err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("stub session: %v", err)
	}
	for _, path := range leftovers {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("session path %s after failure: %v", path, err)
		}
		sibling := filepath.Join(filepath.Dir(path), "existing")
		if data, err := os.ReadFile(sibling); err != nil || string(data) != "keep" {
			t.Fatalf("existing file %s changed: %q, %v", sibling, data, err)
		}
	}
}
