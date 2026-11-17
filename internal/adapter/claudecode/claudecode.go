package claudecode

import (
	"maps"
	"slices"
	"strings"

	"github.com/multiplexer-si/openplexer/internal/adapter/hook"
)

// tools is the table of the tools that the hook gates. Write, Edit, and
// NotebookEdit write the one file that their input names, and Bash runs a
// shell command. Every other tool of Claude Code, such as Read or an MCP tool,
// passes the hook unchecked.
var tools = hook.Tools{
	"Write":        hook.FileTool("file_path"),
	"Edit":         hook.FileTool("file_path"),
	"NotebookEdit": hook.FileTool("notebook_path"),
	"Bash":         hook.ShellTool,
}

// Matcher is the matcher of the PreToolUse hook entry, built from the table so
// that the hook runs for exactly the tools that the adapter gates.
func Matcher() string {
	return strings.Join(slices.Sorted(maps.Keys(tools)), "|")
}

// Handle reads the PreToolUse input that Claude Code writes to the hook and returns
// what the hook process writes and its exit status.
func Handle(stdin []byte, env hook.Env, evaluate hook.Evaluator) hook.Output {
	return hook.Render(hook.Decide(stdin, env, evaluate, tools))
}
