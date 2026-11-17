package claudecode

import "github.com/multiplexer-si/openplexer/internal/adapter/hook"

// Agent is Claude Code 2.1.326. Its hook entry sets onFailure to block, which
// needs 2.1.295 or later, so that a hook that cannot answer blocks the call.
var Agent = hook.Agent{
	Name: "claude-code",
	Tools: hook.Tools{
		"Write":        hook.FileTool("file_path"),
		"Edit":         hook.FileTool("file_path"),
		"NotebookEdit": hook.FileTool("notebook_path"),
		"Bash":         hook.ShellTool,
	},
	ConfigEnv: "CLAUDE_CONFIG_DIR",
	ConfigDir: ".claude",
	HookFile:  "settings.json",
	Settings:  []string{"settings.json", "settings.local.json"},
	Timeout:   5,
	OnFailure: "block",
}
