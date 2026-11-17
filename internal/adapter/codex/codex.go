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

func applyPatch(env hook.Env, call hook.Call) ([]hook.Intent, error) {
	text, err := call.Text("command")
	if err != nil {
		return nil, err
	}
	paths, ok := ParsePatch(text)
	if !ok {
		return nil, errUnreadablePatch
	}
	return env.WriteIntents(call, paths...), nil
}

// Handle reads the PreToolUse input that Codex writes to the hook and returns
// what the hook process writes and its exit status.
func Handle(stdin []byte, env hook.Env, evaluate hook.Evaluator) hook.Output {
	return hook.Render(hook.Decide(stdin, env, evaluate, tools))
}
