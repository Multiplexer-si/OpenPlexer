package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
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
	// Root finds the root of the project that holds a path. The adapter asks
	// it about the working directory of a call and about each file that the
	// call writes, so a write is judged by the project that holds the file.
	// When Root returns an error for the working directory, every write and
	// every command that the policy would judge is denied, and the reason
	// gives the error. A written file with ErrNoRoot is outside every project
	// and is not judged, and any other error for it denies the call. With no
	// Root, a path is read as a path under the working directory, as the
	// reference reads it when it finds no root, and the adapter fixtures run
	// that way.
	Root   func(path string) (string, error)
	Guards []Guard
	Home   string
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
	Event       string
	Tool        string
	Cwd         string
	Session     string
	Input       map[string]json.RawMessage
	ProjectHome bool
}

// ParseCall reads the JSON that a coding agent writes to a hook.
func ParseCall(stdin []byte) (Call, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(stdin, &raw); err != nil {
		return Call{}, fmt.Errorf("the hook input is not a JSON object: %w", err)
	}
	if raw == nil {
		return Call{}, errors.New("the hook input is not a JSON object")
	}
	var call Call
	for _, field := range []struct {
		name string
		dest *string
	}{
		{"hook_event_name", &call.Event}, {"tool_name", &call.Tool},
		{"cwd", &call.Cwd}, {"session_id", &call.Session},
	} {
		if value, ok := raw[field.name]; ok {
			if err := json.Unmarshal(value, field.dest); err != nil {
				return Call{}, fmt.Errorf("invalid %s: %w", field.name, err)
			}
		}
	}
	if input, ok := raw["tool_input"]; ok {
		if err := json.Unmarshal(input, &call.Input); err != nil {
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

type Effect struct {
	Writes   []string
	Targets  []WriteTarget
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
		if call.ProjectHome {
			return Effect{Targets: []WriteTarget{{Path: path}}}, nil
		}
		return Effect{Writes: []string{path}}, nil
	}
}

func ShellTool(call Call) (Effect, error) {
	command, err := call.Text("command")
	if err != nil {
		return Effect{}, err
	}
	var effect Effect
	if call.ProjectHome {
		effect.Targets, err = projectShellTargets(command)
		if err != nil {
			return Effect{}, err
		}
	} else {
		effect.Writes = WriteTargets(command)
		for _, path := range MovedPaths(command) {
			if !slices.Contains(effect.Writes, path) {
				effect.Writes = append(effect.Writes, path)
			}
		}
	}
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

type target struct {
	named      string
	absolute   string
	root       string
	policyPath string
	judged     bool
	unplaced   error
}

func (e Env) place(cwd string, writes []string) []target {
	var out []target
	for _, write := range writes {
		t := target{named: write, absolute: e.absolute(write, cwd)}
		if e.Root == nil {
			t.policyPath = normalizePath(write, cwd)
			t.judged = inProject(t.policyPath)
		} else if root, err := e.Root(t.absolute); err == nil {
			rel, _ := filepath.Rel(root, t.absolute)
			t.root, t.policyPath, t.judged = root, filepath.ToSlash(rel), true
		} else if !errors.Is(err, ErrNoRoot) {
			t.unplaced = err
		}
		out = append(out, t)
	}
	return out
}

func (e Env) queries(call Call, targets []target, effect Effect) []policy.Query {
	if e.AgentSessionID == nil && call.Session != "" {
		e.AgentSessionID = &call.Session
	}
	var out []policy.Query
	for _, t := range targets {
		if t.judged {
			out = append(out, e.query(policy.Write, t.policyPath, ""))
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
// and a call to a gated tool that cannot be read, are refused.
func Decide(stdin []byte, env Env, judge Judge, tools Tools) Verdict {
	call, translate, err := coveredCall(stdin, tools)
	if err != nil {
		return Verdict{Refuse, err.Error()}
	}
	if translate == nil {
		return Verdict{}
	}
	effect, err := translate(call)
	if err != nil {
		return Verdict{Refuse, err.Error()}
	}
	var root string
	var noRoot error
	if env.Root != nil {
		root, noRoot = env.Root(call.Cwd)
	}
	targets := env.place(call.Cwd, effect.Writes)
	if v, guarded := env.guard(root, targets); guarded {
		return v
	}
	if noRoot != nil {
		if len(effect.Writes) > 0 || env.ExecGated && len(effect.Commands) > 0 {
			return Verdict{Deny, fmt.Sprintf("OpenPlexer blocked this call because it cannot place the paths of the call in a project: %v.", noRoot)}
		}
		return Verdict{}
	}
	for _, t := range targets {
		if t.unplaced != nil {
			return Verdict{Deny, fmt.Sprintf("OpenPlexer blocked this call because it cannot place %s in a project: %v.", t.named, t.unplaced)}
		}
	}
	queries := env.queries(call, targets, effect)
	if len(queries) == 0 {
		return Verdict{}
	}
	return judge(queries[0], queries[1:]...)
}

func coveredCall(stdin []byte, tools Tools) (Call, Translate, error) {
	call, err := ParseCall(stdin)
	if err != nil {
		return Call{}, nil, err
	}
	translate, gated := tools[call.Tool]
	if call.Event != "PreToolUse" || !gated {
		return call, nil, nil
	}
	if call.Cwd == "" {
		return call, nil, errors.New("the hook input has no cwd, so the paths of the call cannot be placed in the project")
	}
	return call, translate, nil
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
