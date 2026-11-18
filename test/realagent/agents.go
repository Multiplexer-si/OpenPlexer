package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/multiplexer-si/openplexer/test/internal/agentcli"
)

const (
	claudeModel = "haiku"
	codexModel  = "gpt-6-luna"
)

type runSetup struct {
	project           string
	state             string
	home              string
	sessionID         string
	codexHooks        []string
	codexSessionHooks []string
}

type agent struct {
	name         string
	installName  string
	model        string
	seed         func(d runSetup) error
	afterInstall func(d *runSetup, offline bool) error
	prepare      func(ctx context.Context, d runSetup, s scenario) (*exec.Cmd, error)
	leftovers    func(d runSetup) ([]string, error)
	feedback     func(stdout, stderr []byte) (said, toolResults string)
}

var agents = []agent{claude, codex}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func versionOf(bin string) (string, error) {
	out, err := exec.Command(bin, "--version").CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

var claude = agent{
	name:        "claude",
	installName: "claude-code",
	model:       claudeModel,
	seed: func(d runSetup) error {
		return agentcli.SeedClaudeSettings(d.project)
	},
	afterInstall: func(d *runSetup, offline bool) error {
		if !offline {
			return nil
		}
		return rewriteClaudeSettings(filepath.Join(d.project, ".claude", "settings.json"))
	},
	prepare: func(ctx context.Context, d runSetup, s scenario) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, "claude", agentcli.ClaudeArgs(d.sessionID, claudeModel, s.claudeTools)...)
		cmd.Dir = d.project
		cmd.Env = agentcli.ClaudeEnv(append(os.Environ(), "OPENPLEXER_HOME="+d.home), d.state)
		cmd.Stdin = strings.NewReader(s.prompt)
		return cmd, nil
	},
	leftovers: func(d runSetup) ([]string, error) {
		config := os.Getenv("CLAUDE_CONFIG_DIR")
		if config == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			config = filepath.Join(home, ".claude")
		}
		return agentcli.ClaudeLeftovers(config, d.project, d.sessionID), nil
	},
	feedback: claudeFeedback,
}

var codex = agent{
	name:        "codex",
	installName: "codex",
	model:       codexModel,
	afterInstall: func(d *runSetup, offline bool) error {
		path := filepath.Join(d.project, ".codex", "hooks.json")
		var err error
		if d.codexHooks, err = agentcli.HooksOverrides(path, nil); err != nil {
			return err
		}
		d.codexSessionHooks = d.codexHooks
		if offline {
			d.codexSessionHooks, err = agentcli.HooksOverrides(path, offlineCommand)
		}
		return err
	},
	prepare: func(ctx context.Context, d runSetup, s scenario) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, "codex", agentcli.CodexArgs(d.project, d.state, codexModel, d.codexSessionHooks)...)
		cmd.Dir = d.project
		cmd.Env = append(os.Environ(), "OPENPLEXER_HOME="+d.home)
		cmd.Stdin = strings.NewReader(s.prompt)
		return cmd, nil
	},
	leftovers: func(runSetup) ([]string, error) { return nil, nil },
	feedback:  codexFeedback,
}
