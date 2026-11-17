package codex

import (
	"testing"

	"github.com/multiplexer-si/openplexer/internal/adapter/hook"
	"github.com/multiplexer-si/openplexer/internal/policy"
)

func TestMatcherNamesTheGatedTools(t *testing.T) {
	if got, want := Matcher(), "Bash|apply_patch"; got != want {
		t.Fatalf("Matcher() = %q, want %q", got, want)
	}
}

func TestUnreadablePatchIsRefusedWithoutAQuery(t *testing.T) {
	evaluate := func(policy.Query) policy.Result {
		t.Fatal("the policy was asked about a patch that cannot be read")
		return policy.Result{}
	}
	input := `{"hook_event_name":"PreToolUse","tool_name":"apply_patch","cwd":"/p","tool_input":{"command":"*** Begin Patch\n*** Add File: a.go"}}`
	if out := Handle([]byte(input), hook.Env{Repo: "app", Member: "m"}, hook.FirstDeny(evaluate)); out.ExitCode != 2 || out.Stderr == "" {
		t.Fatalf("output = %+v, want exit status 2 with a reason", out)
	}
}
