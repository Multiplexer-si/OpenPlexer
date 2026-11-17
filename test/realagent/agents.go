package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var hookEvents = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"}

const (
	claudeModel = "haiku"
	codexModel  = "gpt-6-luna"
)

type runSetup struct {
	project   string
	state     string
	sessionID string
}

type agent struct {
	name      string
	model     string
	prepare   func(d runSetup, s scenario, hookCmd string) (*exec.Cmd, error)
	leftovers func(d runSetup) ([]string, error)
	feedback  func(stdout []byte) (said, toolResults string)
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
	name:  "claude",
	model: claudeModel,
	prepare: func(d runSetup, s scenario, hookCmd string) (*exec.Cmd, error) {
		groups := map[string]any{}
		for _, ev := range hookEvents {
			groups[ev] = []any{map[string]any{"hooks": []any{map[string]any{
				"type": "command", "command": hookCmd, "timeout": 30,
			}}}}
		}
		settings, _ := json.MarshalIndent(map[string]any{
			"hooks": groups,
			"sandbox": map[string]any{
				"enabled":                  true,
				"failIfUnavailable":        true,
				"autoAllowBashIfSandboxed": true,
				"allowUnsandboxedCommands": false,
				"network":                  map[string]any{"allowedDomains": []string{}},
			},
		}, "", "  ")
		if err := os.MkdirAll(filepath.Join(d.project, ".claude"), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(d.project, ".claude", "settings.json"), settings, 0o644); err != nil {
			return nil, err
		}
		cmd := exec.Command("claude", "-p",
			"--session-id", d.sessionID,
			"--model", claudeModel, "--effort", "low",
			"--setting-sources", "project",
			"--tools", s.claudeTools,
			"--permission-mode", "acceptEdits",
			"--permission-prompts", "none",
			"--strict-mcp-config", "--disable-slash-commands", "--no-session-persistence",
			"--output-format", "stream-json", "--verbose", "--include-hook-events")
		cmd.Dir = d.project
		cmd.Env = append(os.Environ(), "CLAUDE_CODE_TMPDIR="+d.state, "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1")
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
	name:  "codex",
	model: codexModel,
	prepare: func(d runSetup, s scenario, hookCmd string) (*exec.Cmd, error) {
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
		for _, ev := range hookEvents {
			args = append(args, "-c", fmt.Sprintf(`hooks.%s=[{hooks=[{type="command",command=%s,timeout=30}]}]`,
				ev, strconv.Quote(hookCmd)))
		}
		cmd := exec.Command("codex", args...)
		cmd.Dir = d.project
		cmd.Stdin = strings.NewReader(s.prompt)
		return cmd, nil
	},
	leftovers: func(runSetup) ([]string, error) { return nil, nil },
	feedback:  codexFeedback,
}
