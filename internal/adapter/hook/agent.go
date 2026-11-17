package hook

import (
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Agent is one coding agent that OpenPlexer connects to through its hooks:
// the tools that its hook judges, and where its hook entry lives.
type Agent struct {
	// Name names the agent on the command line, in the hook command, and in
	// audit records.
	Name  string
	Tools Tools
	// ConfigEnv is the environment variable that moves the user
	// configuration directory of the agent away from ConfigDir in the home
	// directory.
	ConfigEnv string
	// ConfigDir is the name of the configuration directory of the agent, in
	// the home directory and in a project.
	ConfigDir string
	// HookFile is the file of ConfigDir that install writes.
	HookFile string
	// Settings are the files of ConfigDir that can remove the hook or switch
	// it off. No agent may write them.
	Settings  []string
	Timeout   int
	OnFailure string
}

// PreTool is the event word of `openplexer hook <agent> pre-tool`.
const PreTool = "pre-tool"

// Matcher is the matcher of the PreToolUse hook entry, built from the table so
// that the hook runs for exactly the tools that the adapter gates.
func (a Agent) Matcher() string {
	return strings.Join(slices.Sorted(maps.Keys(a.Tools)), "|")
}

// Handle reads the PreToolUse input that the agent writes to the hook, and
// returns what the hook process writes and its exit status.
func (a Agent) Handle(stdin []byte, env Env, judge Judge) Output {
	return Render(Decide(stdin, env, judge, a.Tools))
}

// Handler is the hook entry that install writes for the binary at executable.
func (a Agent) Handler(executable string) Handler {
	return Handler{
		Agent:     a.Name,
		Event:     "PreToolUse",
		Matcher:   a.Matcher(),
		Command:   shellQuote(executable) + " hook " + a.Name + " " + PreTool,
		Timeout:   a.Timeout,
		OnFailure: a.OnFailure,
	}
}

// Scope is where a hook entry applies: to every project of the developer, or
// to one project.
type Scope string

const (
	User    Scope = "user"
	Project Scope = "project"
)

// UserDir is the user configuration directory of the agent.
func (a Agent) UserDir(getenv func(string) string, home string) string {
	if dir := getenv(a.ConfigEnv); dir != "" {
		return dir
	}
	return filepath.Join(home, a.ConfigDir)
}

// File is the hook file of a scope. userDir is the result of UserDir, and
// project is the directory of the project.
func (a Agent) File(scope Scope, userDir, project string) string {
	if scope == Project {
		return filepath.Join(project, a.ConfigDir, a.HookFile)
	}
	return filepath.Join(userDir, a.HookFile)
}

// Guards are the settings files of the agent, in userDir and in the project
// root.
func (a Agent) Guards(userDir string) []Guard {
	var guards []Guard
	for _, name := range a.Settings {
		why := "it configures the hooks of " + a.Name + ", and a change to it can remove the OpenPlexer hook"
		guards = append(guards,
			Guard{Path: filepath.Join(userDir, name), Why: why},
			Guard{Path: filepath.Join(a.ConfigDir, name), Why: why})
	}
	return guards
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_./+:@%=-]+$`)

func shellQuote(word string) string {
	if plainWord.MatchString(word) {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}
