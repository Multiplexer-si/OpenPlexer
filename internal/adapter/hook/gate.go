package hook

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/multiplexer-si/openplexer/internal/policy"
)

// Env is what the caller knows and the hook input does not. The adapter builds
// every policy query from it and from the tool call.
type Env struct {
	Repo   string
	Member string
	Now    policy.Timestamp
	// AgentSessionID, MissionID, and Overrides reach each query as they are.
	AgentSessionID *string
	MissionID      *string
	Overrides      []policy.Override
	// ProjectRoot is the absolute path of the project. With none, a path is
	// read as a path under the working directory of the hook call.
	ProjectRoot string
	// ExecGated is set when the role of the member lists commands. A shell
	// command is then also judged as `exec`. Otherwise the commands of the
	// member are not limited, and the command is judged by its write targets.
	ExecGated bool
}

// ExecGated reports whether the role of member in bundle lists commands.
func ExecGated(bundle *policy.Bundle, member string) bool {
	return len(bundle.Roles[bundle.Members[member]].Exec) > 0
}

// Intent is one thing that a tool call does and that the policy judges: a
// write to a path of the project, or the run of a command.
type Intent struct {
	Action  policy.Action
	Path    string
	Command string
}

func (e Env) query(i Intent) policy.Query {
	return policy.Query{
		Repo:           e.Repo,
		Path:           i.Path,
		Action:         i.Action,
		Member:         e.Member,
		Command:        i.Command,
		AgentSessionID: e.AgentSessionID,
		MissionID:      e.MissionID,
		Overrides:      e.Overrides,
		Now:            e.Now,
	}
}

// Evaluator answers one policy query. The caller supplies it, so that the gate
// of the daemon decides what a query is judged against.
type Evaluator func(policy.Query) policy.Result

// Call is the part of a hook input that every agent sends.
type Call struct {
	Event string
	Tool  string
	Cwd   string
	Input map[string]json.RawMessage
}

// ParseCall reads the JSON that a coding agent writes to a hook.
func ParseCall(stdin []byte) (Call, error) {
	var raw struct {
		Event string          `json:"hook_event_name"`
		Tool  string          `json:"tool_name"`
		Cwd   string          `json:"cwd"`
		Input json.RawMessage `json:"tool_input"`
	}
	if err := json.Unmarshal(stdin, &raw); err != nil {
		return Call{}, fmt.Errorf("the hook input is not a JSON object: %w", err)
	}
	call := Call{Event: raw.Event, Tool: raw.Tool, Cwd: raw.Cwd}
	if len(raw.Input) > 0 {
		if err := json.Unmarshal(raw.Input, &call.Input); err != nil {
			return Call{}, fmt.Errorf("tool_input is not a JSON object: %w", err)
		}
	}
	return call, nil
}

// Text returns the string field of the tool input.
func (c Call) Text(field string) (string, error) {
	var text string
	if err := json.Unmarshal(c.Input[field], &text); err != nil {
		return "", fmt.Errorf("%s of a %s call has no string value", field, c.Tool)
	}
	return text, nil
}

// Translate turns the call to one tool into its intents. An error means that
// the call names a target that the adapter cannot read.
type Translate func(env Env, call Call) ([]Intent, error)

// Tools maps a tool name to the translation of its calls. A tool that the map
// does not name is not gated.
type Tools map[string]Translate

// WriteIntents makes a write intent of each path that is inside the project.
func (e Env) WriteIntents(call Call, paths ...string) []Intent {
	var out []Intent
	for _, path := range paths {
		if rel := normalizeTarget(path, call.Cwd, e.ProjectRoot); inProject(rel) {
			out = append(out, Intent{Action: policy.Write, Path: rel})
		}
	}
	return out
}

// FileTool translates a call to a tool that writes the one file that an input
// field names.
func FileTool(field string) Translate {
	return func(env Env, call Call) ([]Intent, error) {
		path, err := call.Text(field)
		if err != nil {
			return nil, err
		}
		return env.WriteIntents(call, path), nil
	}
}

// ShellTool translates a call to a tool that runs the command in the input
// field `command`. The intents are the writes that the command is clear to
// make. For a member whose role lists commands, they also hold the commands.
// That is the whole line, if it holds a substitution that no grant can cover,
// and otherwise each command of the line that is not a bare `tee`.
func ShellTool(env Env, call Call) ([]Intent, error) {
	command, err := call.Text("command")
	if err != nil {
		return nil, err
	}
	intents := env.WriteIntents(call, WriteTargets(command)...)
	if !env.ExecGated {
		return intents, nil
	}
	if substitution.MatchString(command) {
		return append(intents, Intent{Action: policy.Exec, Command: command}), nil
	}
	for _, words := range Segments(command) {
		if !bareTee(words) {
			intents = append(intents, Intent{Action: policy.Exec, Command: strings.Join(words, " ")})
		}
	}
	return intents, nil
}

// Denial is the first intent that the policy denies, with its result.
type Denial struct {
	Intent Intent
	Result policy.Result
}

// Reason is the sentence that the coding agent shows. It names the intent and
// the rule that denied it.
func (d Denial) Reason() string {
	where := ""
	if d.Result.Area != nil {
		where = fmt.Sprintf(" in area %q", *d.Result.Area)
	}
	if d.Intent.Action == policy.Exec {
		return fmt.Sprintf("OpenPlexer policy denied the command %q%s: %s. Ask the owner of the policy to grant it.", d.Intent.Command, where, d.Result.Rule)
	}
	return fmt.Sprintf("OpenPlexer policy denied a write to %s%s: %s. Ask the owner of the policy to grant it.", d.Intent.Path, where, d.Result.Rule)
}

// Check asks the evaluator about each intent in order and returns the first
// denial, or nil if every intent is allowed.
func (e Env) Check(intents []Intent, evaluate Evaluator) *Denial {
	for _, i := range intents {
		if result := evaluate(e.query(i)); result.Decision == policy.Deny {
			return &Denial{Intent: i, Result: result}
		}
	}
	return nil
}

// Outcome is what an adapter does with a tool call.
type Outcome int

const (
	// Allow lets the call run, and the adapter writes nothing.
	Allow Outcome = iota
	// Deny stops the call because the policy denies it.
	Deny
	// Refuse stops the call because the adapter cannot tell what it does.
	Refuse
)

// Verdict is the outcome of one hook call, with the sentence for a person.
type Verdict struct {
	Outcome Outcome
	Reason  string
}

// Decide reads one hook input and judges it. An event other than PreToolUse,
// and a tool that the table does not name, are allowed. Input that is not JSON,
// and a call to a gated tool that cannot be read, are refused.
func Decide(stdin []byte, env Env, evaluate Evaluator, tools Tools) Verdict {
	call, err := ParseCall(stdin)
	if err != nil {
		return Verdict{Refuse, err.Error()}
	}
	translate, gated := tools[call.Tool]
	if call.Event != "PreToolUse" || !gated {
		return Verdict{}
	}
	if call.Cwd == "" {
		return Verdict{Refuse, "the hook input has no cwd, so the paths of the call cannot be placed in the project"}
	}
	intents, err := translate(env, call)
	if err != nil {
		return Verdict{Refuse, err.Error()}
	}
	if denial := env.Check(intents, evaluate); denial != nil {
		return Verdict{Deny, denial.Reason()}
	}
	return Verdict{}
}

// Output is what the adapter makes the hook process do.
type Output struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type preToolDeny struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// Render turns a verdict into the output of a PreToolUse hook. Claude Code and
// Codex read the same deny verdict. An allowed call gets no output and exit
// status 0, which leaves the permission flow of the agent as it is. A denied
// call gets the deny verdict with the reason. A refused call gets exit status
// 2 and the reason on standard error, which both agents read as a block.
func Render(v Verdict) Output {
	switch v.Outcome {
	case Deny:
		var body preToolDeny
		body.HookSpecificOutput.HookEventName = "PreToolUse"
		body.HookSpecificOutput.PermissionDecision = "deny"
		body.HookSpecificOutput.PermissionDecisionReason = v.Reason
		text, _ := json.Marshal(body)
		return Output{Stdout: string(text) + "\n"}
	case Refuse:
		return Output{Stderr: "openplexer: " + v.Reason + "\n", ExitCode: 2}
	}
	return Output{}
}
