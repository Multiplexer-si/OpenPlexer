package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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
	signedIn     func() error
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

var nonAlphanumeric = regexp.MustCompile(`[^A-Za-z0-9]`)

func newSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func claudeSlug(dir string) string {
	return nonAlphanumeric.ReplaceAllString(dir, "-")
}

var claude = agent{
	name:        "claude",
	installName: "claude-code",
	model:       claudeModel,
	seed: func(d runSetup) error {
		settings, _ := json.MarshalIndent(map[string]any{
			"sandbox": map[string]any{
				"enabled":                  true,
				"failIfUnavailable":        true,
				"autoAllowBashIfSandboxed": true,
				"allowUnsandboxedCommands": false,
				"network":                  map[string]any{"allowedDomains": []string{}},
			},
		}, "", "  ")
		if err := os.MkdirAll(filepath.Join(d.project, ".claude"), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(d.project, ".claude", "settings.json"), settings, 0o644)
	},
	afterInstall: func(d *runSetup, offline bool) error {
		if !offline {
			return nil
		}
		return rewriteClaudeSettings(filepath.Join(d.project, ".claude", "settings.json"))
	},
	signedIn: func() error {
		out, runErr := exec.Command("claude", "auth", "status").Output()
		var status struct {
			LoggedIn bool `json:"loggedIn"`
		}
		if err := json.Unmarshal(out, &status); err != nil {
			return fmt.Errorf("claude auth status: %w", errors.Join(runErr, err))
		}
		if !status.LoggedIn {
			return errors.New("claude auth status reports no sign-in; run `claude auth login` in a plain terminal")
		}
		return nil
	},
	prepare: func(ctx context.Context, d runSetup, s scenario) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, "claude", "-p",
			"--session-id", d.sessionID,
			"--model", claudeModel, "--effort", "low",
			"--setting-sources", "project",
			"--tools", s.claudeTools,
			"--permission-mode", "acceptEdits",
			"--permission-prompts", "none",
			"--strict-mcp-config", "--disable-slash-commands", "--no-session-persistence",
			"--output-format", "stream-json", "--verbose", "--include-hook-events")
		cmd.Dir = d.project
		cmd.Env = append(os.Environ(), "CLAUDE_CODE_TMPDIR="+d.state, "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1", "OPENPLEXER_HOME="+d.home)
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
		return []string{
			filepath.Join(config, "projects", claudeSlug(d.project)),
			filepath.Join(config, "session-env", d.sessionID),
		}, nil
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
		if d.codexHooks, err = hooksOverrides(path, nil); err != nil {
			return err
		}
		d.codexSessionHooks = d.codexHooks
		if offline {
			d.codexSessionHooks, err = hooksOverrides(path, offlineCommand)
		}
		return err
	},
	signedIn: func() error {
		if out, err := exec.Command("codex", "login", "status").CombinedOutput(); err != nil {
			return fmt.Errorf("codex login status: %w: %s; run `codex login` in a plain terminal", err, strings.TrimSpace(string(out)))
		}
		return nil
	},
	prepare: func(ctx context.Context, d runSetup, s scenario) (*exec.Cmd, error) {
		args := []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check",
			"--ignore-user-config", "--ignore-rules", "--dangerously-bypass-hook-trust",
			"--disable", "plugins", "--disable", "remote_plugin", "--disable", "apps", "--disable", "shell_snapshot",
			"-m", codexModel, "-c", `model_reasoning_effort="low"`,
			"-s", "workspace-write",
			"-c", "sandbox_workspace_write.network_access=false",
			"-c", "sandbox_workspace_write.exclude_tmpdir_env_var=true",
			"-c", "sandbox_workspace_write.exclude_slash_tmp=true",
			"-c", `approval_policy="never"`,
			"-c", "sqlite_home=" + strconv.Quote(d.state),
			"-c", "log_dir=" + strconv.Quote(filepath.Join(d.state, "log")),
			"-C", d.project}
		cmd := exec.CommandContext(ctx, "codex", append(args, d.codexSessionHooks...)...)
		cmd.Dir = d.project
		cmd.Env = append(os.Environ(), "OPENPLEXER_HOME="+d.home)
		cmd.Stdin = strings.NewReader(s.prompt)
		return cmd, nil
	},
	leftovers: func(runSetup) ([]string, error) { return nil, nil },
	feedback:  codexFeedback,
}
