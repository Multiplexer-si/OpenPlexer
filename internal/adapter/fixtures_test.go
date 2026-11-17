package adapter_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/adapter/claudecode"
	"github.com/multiplexer-si/openplexer/internal/adapter/codex"
	"github.com/multiplexer-si/openplexer/internal/adapter/hook"
	"github.com/multiplexer-si/openplexer/internal/policy"
)

const fixtureDir = "../../spec/adapter/v1/fixtures"

type fixtureFile struct {
	Operation string `json:"operation"`
	Shared    struct {
		Agent  string          `json:"agent"`
		Bundle json.RawMessage `json:"bundle"`
		Repo   string          `json:"repo"`
		Now    string          `json:"now"`
	} `json:"shared"`
	Cases []fixtureCase `json:"cases"`
}

type fixtureCase struct {
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	Expected json.RawMessage `json:"expected"`
}

var handlers = map[string]func([]byte, hook.Env, hook.Evaluator) hook.Output{
	"claude-code": claudecode.Handle,
	"codex":       codex.Handle,
}

func readFixture(t *testing.T, path string) fixtureFile {
	t.Helper()
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file fixtureFile
	if err := json.Unmarshal(text, &file); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if len(file.Cases) == 0 {
		t.Fatalf("%s: no cases", path)
	}
	return file
}

func TestFixtures(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(fixtureDir, "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("found no fixture under %s: %v", fixtureDir, err)
	}
	ran := 0
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			file := readFixture(t, path)
			var run func(t *testing.T, c fixtureCase)
			switch file.Operation {
			case "bash-targets":
				run = runBashTargets
			case "exec-segments":
				run = runExecSegments
			case "patch-targets":
				run = runPatchTargets
			case "pre-tool":
				run = preToolRunner(t, file)
			default:
				t.Fatalf("unknown operation %q", file.Operation)
			}
			for _, c := range file.Cases {
				ran++
				t.Run(c.Name, func(t *testing.T) { run(t, c) })
			}
		})
	}
	t.Logf("ran %d cases", ran)
}

func decode(t *testing.T, text json.RawMessage, into any) {
	t.Helper()
	if err := json.Unmarshal(text, into); err != nil {
		t.Fatal(err)
	}
}

func runBashTargets(t *testing.T, c fixtureCase) {
	var input struct{ Command string }
	var want struct{ Targets []string }
	decode(t, c.Input, &input)
	decode(t, c.Expected, &want)
	if got := hook.WriteTargets(input.Command); !slices.Equal(got, want.Targets) {
		t.Fatalf("targets = %q, want %q", got, want.Targets)
	}
}

func runExecSegments(t *testing.T, c fixtureCase) {
	var input struct{ Command string }
	var want struct{ Segments [][]string }
	decode(t, c.Input, &input)
	decode(t, c.Expected, &want)
	if got := hook.Segments(input.Command); !slices.EqualFunc(got, want.Segments, slices.Equal) {
		t.Fatalf("segments = %q, want %q", got, want.Segments)
	}
}

func runPatchTargets(t *testing.T, c fixtureCase) {
	var input struct{ Patch string }
	var want struct{ Targets *[]string }
	decode(t, c.Input, &input)
	decode(t, c.Expected, &want)
	got, ok := codex.ParsePatch(input.Patch)
	switch {
	case want.Targets == nil && ok:
		t.Fatalf("targets = %q, want a patch that Codex rejects", got)
	case want.Targets != nil && !ok:
		t.Fatalf("a patch that Codex accepts was rejected, want %q", *want.Targets)
	case ok && !slices.Equal(got, *want.Targets):
		t.Fatalf("targets = %q, want %q", got, *want.Targets)
	}
}

func preToolRunner(t *testing.T, file fixtureFile) func(*testing.T, fixtureCase) {
	handle, ok := handlers[file.Shared.Agent]
	if !ok {
		t.Fatalf("no adapter for the agent %q", file.Shared.Agent)
	}
	bundle, err := policy.ParseBundle(file.Shared.Bundle)
	if err != nil {
		t.Fatalf("shared bundle: %v", err)
	}
	now, ok := policy.ParseTimestamp(file.Shared.Now)
	if !ok {
		t.Fatalf("shared now %q is not a timestamp", file.Shared.Now)
	}
	return func(t *testing.T, c fixtureCase) {
		var input struct {
			Member string          `json:"member"`
			Hook   json.RawMessage `json:"hook"`
		}
		var want struct{ Decision string }
		decode(t, c.Input, &input)
		decode(t, c.Expected, &want)
		env := hook.Env{
			Repo:      file.Shared.Repo,
			Member:    input.Member,
			Now:       now,
			ExecGated: hook.ExecGated(bundle, input.Member),
		}
		out := handle(input.Hook, env, func(q policy.Query) policy.Result { return policy.Evaluate(bundle, q) })
		if got := decisionOf(t, out); got != want.Decision {
			t.Fatalf("decision = %s, want %s (output %+v)", got, want.Decision, out)
		}
	}
}

func decisionOf(t *testing.T, out hook.Output) string {
	t.Helper()
	switch {
	case out.ExitCode == 2 && out.Stderr != "":
		return "deny"
	case out.ExitCode == 0 && out.Stdout == "" && out.Stderr == "":
		return "allow"
	case out.ExitCode == 0 && out.Stderr == "":
		var verdict struct {
			HookSpecificOutput struct {
				HookEventName            string `json:"hookEventName"`
				PermissionDecision       string `json:"permissionDecision"`
				PermissionDecisionReason string `json:"permissionDecisionReason"`
			} `json:"hookSpecificOutput"`
		}
		decode(t, json.RawMessage(out.Stdout), &verdict)
		body := verdict.HookSpecificOutput
		if body.HookEventName != "PreToolUse" || body.PermissionDecision != "deny" || body.PermissionDecisionReason == "" {
			t.Fatalf("the output is not a deny verdict with a reason: %s", out.Stdout)
		}
		return "deny"
	}
	t.Fatalf("the output is neither silence nor a block: %+v", out)
	return ""
}
