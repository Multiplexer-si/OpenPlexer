package hook

import (
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

type Agent struct {
	Name         string
	Tools        Tools
	UserDirEnv   string
	ConfigDir    string
	HookFile     string
	GuardedFiles []string
	Timeout      int
	OnFailure    string
	NextStep     string
}

const (
	PreTool           = "pre-tool"
	SessionStart      = "session-start"
	sessionStartEvent = "SessionStart"
)

func (a Agent) Matcher() string {
	return strings.Join(slices.Sorted(maps.Keys(a.Tools)), "|")
}

func (a Agent) Handle(stdin []byte, env Env, judge Judge) Output {
	return Render(Decide(stdin, env, judge, a.Tools))
}

// HandleProject passes a covered call and its translator to the portable check.
func (a Agent) HandleProject(stdin []byte, check func(Call, Translate) Verdict) Output {
	call, translate, err := coveredCall(stdin, a.Tools)
	if err != nil {
		return Render(Verdict{Refuse, err.Error()})
	}
	if translate == nil {
		return Output{}
	}
	call.ProjectHome = true
	return Render(check(call, translate))
}

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

func (a Agent) SessionHandler(executable string) Handler {
	return Handler{
		Agent:   a.Name,
		Event:   sessionStartEvent,
		Matcher: "startup|resume",
		Command: shellQuote(executable) + " hook " + a.Name + " " + SessionStart,
		Timeout: a.Timeout,
	}
}

func (a Agent) UserDir(getenv func(string) string, home string) string {
	if dir := getenv(a.UserDirEnv); dir != "" {
		return dir
	}
	return filepath.Join(home, a.ConfigDir)
}

func (a Agent) UserFile(userDir string) string {
	return filepath.Join(userDir, a.HookFile)
}

func (a Agent) ProjectFile(project string) string {
	return filepath.Join(project, a.ConfigDir, a.HookFile)
}

func (a Agent) Guards(userDir string) []Guard {
	var guards []Guard
	for _, name := range a.GuardedFiles {
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
