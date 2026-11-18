package agentcli

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

func SeedClaudeSettings(project string) error {
	const settings = `{
  "sandbox": {
    "enabled": true,
    "failIfUnavailable": true,
    "autoAllowBashIfSandboxed": true,
    "allowUnsandboxedCommands": false,
    "network": {"allowedDomains": []}
  }
}`
	dir := filepath.Join(project, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644)
}

func ClaudeArgs(sessionID, model, tools string) []string {
	return []string{"-p",
		"--session-id", sessionID,
		"--model", model, "--effort", "low",
		"--setting-sources", "project",
		"--tools", tools,
		"--permission-mode", "acceptEdits",
		"--permission-prompts", "none",
		"--strict-mcp-config", "--disable-slash-commands", "--no-session-persistence",
		"--output-format", "stream-json", "--verbose", "--include-hook-events"}
}

func ClaudeEnv(env []string, state string) []string {
	return append(env, "CLAUDE_CODE_TMPDIR="+state, "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1")
}

func NewSessionID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

var nonAlphanumeric = regexp.MustCompile(`[^A-Za-z0-9]`)

func claudeSlug(dir string) string {
	return nonAlphanumeric.ReplaceAllString(dir, "-")
}

func ClaudeLeftovers(config, project, sessionID string) []string {
	return []string{
		filepath.Join(config, "projects", claudeSlug(project)),
		filepath.Join(config, "session-env", sessionID),
	}
}

func RequireAbsent(paths []string) error {
	for _, path := range paths {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("%s already exists; refusing to start the agent", path)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
