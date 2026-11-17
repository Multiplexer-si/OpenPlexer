package codex

import (
	"errors"

	"github.com/multiplexer-si/openplexer/internal/adapter/hook"
)

// Agent is Codex 0.167.1. A shell command reaches its hook as `Bash`, and a
// file edit as `apply_patch`. Codex has no option that blocks a call when the
// hook fails.
var Agent = hook.Agent{
	Name: "codex",
	Tools: hook.Tools{
		"Bash":        hook.ShellTool,
		"apply_patch": applyPatch,
	},
	ConfigEnv: "CODEX_HOME",
	ConfigDir: ".codex",
	HookFile:  "hooks.json",
	Settings:  []string{"hooks.json", "config.toml"},
	Timeout:   5,
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
