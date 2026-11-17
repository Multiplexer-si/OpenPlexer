package hook

import (
	"slices"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/policy"
)

func TestOneCallIsJudgedOnceWithEveryQuery(t *testing.T) {
	var calls [][]policy.Query
	judge := func(first policy.Query, rest ...policy.Query) Verdict {
		calls = append(calls, append([]policy.Query{first}, rest...))
		return Verdict{}
	}
	call := []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"/p","tool_input":{"command":"touch a b && go test"}}`)
	Decide(call, Env{ExecGated: true}, judge, Tools{"Bash": ShellTool})
	if len(calls) != 1 {
		t.Fatalf("the judge was called %d times, want once", len(calls))
	}
	var got []string
	for _, q := range calls[0] {
		got = append(got, string(q.Action)+" "+q.Path+q.Command)
	}
	if want := []string{"write a", "write b", "exec touch a b", "exec go test"}; !slices.Equal(got, want) {
		t.Fatalf("queries = %q, want %q", got, want)
	}
}

func TestACallWithNoQueryIsNotJudged(t *testing.T) {
	judge := func(policy.Query, ...policy.Query) Verdict {
		t.Fatal("the judge was asked about a call that makes no query")
		return Verdict{}
	}
	call := []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"/p","tool_input":{"command":"ls"}}`)
	if v := Decide(call, Env{}, judge, Tools{"Bash": ShellTool}); v.Outcome != Allow {
		t.Fatalf("verdict = %+v, want allow", v)
	}
}
