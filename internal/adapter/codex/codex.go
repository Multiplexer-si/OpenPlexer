package codex

import (
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/multiplexer-si/openplexer/internal/adapter/hook"
)

// A shell command reaches the hook as `Bash`, and a file edit as `apply_patch`.
var tools = hook.Tools{
	"Bash":        hook.ShellTool,
	"apply_patch": applyPatch,
}

// Matcher is the matcher of the PreToolUse hook entry, built from the table so
// that the hook runs for exactly the tools that the adapter gates.
func Matcher() string {
	return strings.Join(slices.Sorted(maps.Keys(tools)), "|")
}

var errUnreadablePatch = errors.New("this apply_patch could not be checked: it is not a patch that the gate can read (*** Begin Patch, then Add, Update, or Delete File hunks, then *** End Patch)")

func applyPatch(call hook.Call) (hook.Effect, error) {
	text, err := call.Text("command")
	if err != nil {
		return hook.Effect{}, err
	}
	paths, ok := ParsePatch(text)
	if !ok {
		return hook.Effect{}, errUnreadablePatch
	}
	return hook.Effect{Writes: paths}, nil
}

// Handle reads the PreToolUse input that Codex writes to the hook and returns
// what the hook process writes and its exit status.
func Handle(stdin []byte, env hook.Env, judge hook.Judge) hook.Output {
	return hook.Render(hook.Decide(stdin, env, judge, tools))
}
