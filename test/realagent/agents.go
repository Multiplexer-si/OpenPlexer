package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var hookEvents = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"}

const (
	claudeModel = "haiku"
	codexModel  = "gpt-6-luna"
)

type agent struct {
	name     string
	binary   string
	version  func() (string, error)
	prepare  func(dir, hookCmd, prompt string) (*exec.Cmd, error)
	feedback func(stdout, stderr []byte) (said, toolResults string)
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
	name:    "claude",
	binary:  "claude",
	version: func() (string, error) { return versionOf("claude") },
	prepare: func(dir, hookCmd, prompt string) (*exec.Cmd, error) {
		groups := map[string]any{}
		for _, ev := range hookEvents {
			groups[ev] = []any{map[string]any{"hooks": []any{map[string]any{
				"type": "command", "command": hookCmd, "timeout": 30,
			}}}}
		}
		settings, _ := json.MarshalIndent(map[string]any{"hooks": groups}, "", "  ")
		if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), settings, 0o644); err != nil {
			return nil, err
		}
		cmd := exec.Command("claude", "-p",
			"--model", claudeModel, "--effort", "low",
			"--setting-sources", "project",
			"--permission-mode", "acceptEdits",
			"--allowedTools", "Write", "Edit", "Bash",
			"--strict-mcp-config", "--disable-slash-commands", "--no-session-persistence",
			"--output-format", "stream-json", "--verbose", "--include-hook-events")
		cmd.Dir = dir
		cmd.Stdin = strings.NewReader(prompt)
		return cmd, nil
	},
	feedback: claudeFeedback,
}

var codex = agent{
	name:    "codex",
	binary:  "codex",
	version: func() (string, error) { return versionOf("codex") },
	prepare: func(dir, hookCmd, prompt string) (*exec.Cmd, error) {
		var entries []string
		for _, ev := range hookEvents {
			entries = append(entries, fmt.Sprintf(`hooks.%s=[{hooks=[{type="command",command=%s,timeout=30}]}]`,
				ev, strconv.Quote(hookCmd)))
		}
		args := []string{"exec", "--json", "--skip-git-repo-check", "--ephemeral",
			"--ignore-user-config", "--dangerously-bypass-hook-trust",
			"-m", codexModel, "-c", `model_reasoning_effort="low"`,
			"-s", "workspace-write", "-C", dir}
		for _, e := range entries {
			args = append(args, "-c", e)
		}
		cmd := exec.Command("codex", args...)
		cmd.Dir = dir
		cmd.Stdin = strings.NewReader(prompt)
		return cmd, nil
	},
	feedback: codexFeedback,
}
