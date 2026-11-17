package claudecode

import (
	"testing"

	"github.com/multiplexer-si/openplexer/internal/adapter/hook"
	"github.com/multiplexer-si/openplexer/internal/policy"
)

func TestMatcherNamesTheGatedTools(t *testing.T) {
	if got, want := Matcher(), "Bash|Edit|NotebookEdit|Write"; got != want {
		t.Fatalf("Matcher() = %q, want %q", got, want)
	}
}

// A notebook cell edit writes the notebook file, so it is judged as a write.
func TestNotebookEditIsAWrite(t *testing.T) {
	var asked []policy.Query
	evaluate := func(q policy.Query) policy.Result {
		asked = append(asked, q)
		return policy.Result{Decision: policy.Deny, Rule: "no"}
	}
	input := `{"hook_event_name":"PreToolUse","tool_name":"NotebookEdit","cwd":"/p","tool_input":{"notebook_path":"/p/data/a.ipynb","new_source":"x"}}`
	out := Handle([]byte(input), hook.Env{Repo: "app", Member: "m"}, evaluate)
	if len(asked) != 1 || asked[0].Action != policy.Write || asked[0].Path != "data/a.ipynb" {
		t.Fatalf("queries = %+v, want one write of data/a.ipynb", asked)
	}
	if out.Stdout == "" || out.ExitCode != 0 {
		t.Fatalf("output = %+v, want a deny verdict", out)
	}
}
