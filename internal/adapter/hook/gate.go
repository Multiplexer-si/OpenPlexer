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
	Repo           string
	Member         string
	Now            policy.Timestamp
	AgentSessionID *string
	MissionID      *string
	Overrides      []policy.Override
	// Root finds the project root above the working directory of a call.
	// When it finds none, every write and every command that the policy would
	// judge is denied. With no Root, a path is read as a path under the
	// working directory.
	Root func(cwd string) (string, bool)
	// Guards are the paths that no agent may write through a gated tool,
	// whatever the policy says.
	Guards []Guard
	// Home is the home directory of the developer. A guard reads a leading ~
	// of a path as it.
	Home string
	// ExecGated is set when the role of the member lists commands. A shell
	// command is then also judged as `exec`. Otherwise the commands of the
	// member are not limited, and the command is judged by its write targets.
	ExecGated bool
}

// ExecGated reports whether the role of member in bundle lists commands.
func ExecGated(bundle *policy.Bundle, member string) bool {
	return len(bundle.Roles[bundle.Members[member]].Exec) > 0
}

func (e Env) query(action policy.Action, path, command string) policy.Query {
	return policy.Query{
		Repo:           e.Repo,
		Path:           path,
		Action:         action,
		Member:         e.Member,
		Command:        command,
		AgentSessionID: e.AgentSessionID,
		MissionID:      e.MissionID,
		Overrides:      e.Overrides,
		Now:            e.Now,
	}
}

// Call is the part of a hook input that every agent sends.
type Call struct {
	Event   string
	Tool    string
	Cwd     string
	Session string
	Input   map[string]json.RawMessage
}

// ParseCall reads the JSON that a coding agent writes to a hook.
func ParseCall(stdin []byte) (Call, error) {
	var raw struct {
		Event   string          `json:"hook_event_name"`
		Tool    string          `json:"tool_name"`
		Cwd     string          `json:"cwd"`
		Session string          `json:"session_id"`
		Input   json.RawMessage `json:"tool_input"`
	}
	if err := json.Unmarshal(stdin, &raw); err != nil {
		return Call{}, fmt.Errorf("the hook input is not a JSON object: %w", err)
	}
	call := Call{Event: raw.Event, Tool: raw.Tool, Cwd: raw.Cwd, Session: raw.Session}
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

// Effect is what one tool call does, as the call names it and before any
// policy: the paths that it is clear to write, and the commands that it runs.
type Effect struct {
	Writes   []string
	Commands []string
}

// Translate reads what a call to one tool does. An error means that the
// call names a target that the adapter cannot read.
type Translate func(call Call) (Effect, error)

// Tools maps a tool name to the translation of its calls. A tool that the map
// does not name is not gated.
type Tools map[string]Translate

// FileTool translates a call to a tool that writes the one file that an input
// field names.
func FileTool(field string) Translate {
	return func(call Call) (Effect, error) {
		path, err := call.Text(field)
		if err != nil {
			return Effect{}, err
		}
		return Effect{Writes: []string{path}}, nil
	}
}

// ShellTool translates a call to a tool that runs the command in the input
// field `command`. The effect writes the paths that the command is clear to
// write, and runs the whole line if the line holds a substitution, and
// otherwise each command of the line that is not a bare `tee`.
func ShellTool(call Call) (Effect, error) {
	command, err := call.Text("command")
	if err != nil {
		return Effect{}, err
	}
	effect := Effect{Writes: WriteTargets(command)}
	if substitution.MatchString(command) {
		effect.Commands = []string{command}
		return effect, nil
	}
	for _, words := range Segments(command) {
		if !bareTee(words) {
			effect.Commands = append(effect.Commands, strings.Join(words, " "))
		}
	}
	return effect, nil
}

func (e Env) queries(call Call, root string, effect Effect) []policy.Query {
	if e.AgentSessionID == nil && call.Session != "" {
		e.AgentSessionID = &call.Session
	}
	var out []policy.Query
	for _, path := range effect.Writes {
		if rel := normalizeTarget(path, call.Cwd, root); inProject(rel) {
			out = append(out, e.query(policy.Write, rel, ""))
		}
	}
	if e.ExecGated {
		for _, command := range effect.Commands {
			out = append(out, e.query(policy.Exec, "", command))
		}
	}
	return out
}

// Judge decides the queries of one tool call, in the order that the call
// makes them. The adapter calls it only for a call that makes a query.
type Judge func(first policy.Query, rest ...policy.Query) Verdict

// FirstDeny judges each query with evaluate, and denies the call at the first
// query that evaluate denies.
func FirstDeny(evaluate func(policy.Query) policy.Result) Judge {
	return func(first policy.Query, rest ...policy.Query) Verdict {
		for _, q := range append([]policy.Query{first}, rest...) {
			if result := evaluate(q); result.Decision == policy.Deny {
				return Denied(q, result)
			}
		}
		return Verdict{}
	}
}

// Denied is the verdict for a call whose query q the policy denied with
// result. The reason names the action and the rule that denied it.
func Denied(q policy.Query, result policy.Result) Verdict {
	where := ""
	if result.Area != nil {
		where = fmt.Sprintf(" in area %q", *result.Area)
	}
	if q.Action == policy.Exec {
		return Verdict{Deny, fmt.Sprintf("OpenPlexer policy denied the command %q%s: %s. Ask the owner of the policy to grant it.", q.Command, where, result.Rule)}
	}
	return Verdict{Deny, fmt.Sprintf("OpenPlexer policy denied a write to %s%s: %s. Ask the owner of the policy to grant it.", q.Path, where, result.Rule)}
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
// and a call to a gated tool that cannot be read, are refused. A call that
// makes no query is allowed without asking judge.
func Decide(stdin []byte, env Env, judge Judge, tools Tools) Verdict {
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
	effect, err := translate(call)
	if err != nil {
		return Verdict{Refuse, err.Error()}
	}
	root, found := "", env.Root == nil
	if env.Root != nil {
		root, found = env.Root(call.Cwd)
	}
	if v, guarded := env.guard(call.Cwd, root, effect.Writes); guarded {
		return v
	}
	if !found {
		if len(effect.Writes) > 0 || env.ExecGated && len(effect.Commands) > 0 {
			return Verdict{Deny, fmt.Sprintf("OpenPlexer blocked this call because it found no project root above %s: no directory there holds .git, .claude, .codex, or .mcp.json, so the paths of the call cannot be placed in a project. Run the agent inside the project.", call.Cwd)}
		}
		return Verdict{}
	}
	queries := env.queries(call, root, effect)
	if len(queries) == 0 {
		return Verdict{}
	}
	return judge(queries[0], queries[1:]...)
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
