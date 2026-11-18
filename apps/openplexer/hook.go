package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/multiplexer-si/openplexer/internal/adapter"
	"github.com/multiplexer-si/openplexer/internal/adapter/hook"
	"github.com/multiplexer-si/openplexer/internal/daemon"
	"github.com/multiplexer-si/openplexer/internal/policy"
)

// Codex lets a call run when its hook times out, so the hook blocks the call
// itself first.
var hookBudget = 4 * time.Second

// Exit status 2 with a reason on standard error blocks the call in both
// agents.
var abort = func() { os.Exit(2) }

func (c command) hook(agent hook.Agent) int {
	watchdog := time.AfterFunc(hookBudget, func() {
		fmt.Fprintf(c.stderr, "openplexer: the hook did not answer within %s, so it blocks this call\n", hookBudget)
		abort()
	})
	defer watchdog.Stop()

	input, err := io.ReadAll(c.stdin)
	if err != nil {
		return c.emit(hook.Render(hook.Verdict{Outcome: hook.Refuse, Reason: "cannot read the hook input: " + err.Error()}))
	}
	userHome, _ := os.UserHomeDir()
	gate, openErr := daemon.Open(c.home, c.now)
	roleUnknown := openErr != nil || !gate.Status().Enforced()
	env := hook.Env{
		ExecGated: roleUnknown || gate.ExecGated(),
		Root:      func(cwd string) (string, bool) { return hook.FindRoot(cwd, userHome) },
		Guards:    c.guards(userHome),
		Home:      userHome,
	}
	return c.emit(agent.Handle(input, env, c.judge(agent.Name, gate, openErr)))
}

func (c command) emit(out hook.Output) int {
	io.WriteString(c.stdout, out.Stdout)
	io.WriteString(c.stderr, out.Stderr)
	return out.ExitCode
}

func (c command) guards(userHome string) []hook.Guard {
	home, err := filepath.Abs(c.home)
	if err != nil {
		home = c.home
	}
	guards := []hook.Guard{{Path: home, Why: "it holds the policy, the key set, and the audit log of OpenPlexer"}}
	for _, agent := range adapter.Agents {
		guards = append(guards, agent.Guards(agent.UserDir(os.Getenv, userHome))...)
	}
	return guards
}

func (c command) judge(agent string, gate *daemon.Gate, openErr error) hook.Judge {
	return func(first policy.Query, rest ...policy.Query) hook.Verdict {
		if openErr != nil {
			return blocked("OpenPlexer cannot read its configuration in %s, so it blocks this call: %v. Run `openplexer policy status` to see the problem.", c.home, openErr)
		}
		verdict, err := gate.Decide(agent, first, rest...)
		result, decided := verdict.Result()
		switch {
		case !decided && verdict.Policy.State == daemon.Absent:
			return blocked("OpenPlexer has no policy loaded, so it blocks this call. Load a signed policy with `openplexer policy load <file>`, or remove the hook with `openplexer uninstall %s`.", agent)
		case !decided:
			return blocked("OpenPlexer cannot verify its policy, so it blocks this call: %s. Run `openplexer policy status`, then load a policy that verifies.", verdict.Policy.Detail)
		case result.Decision == policy.Deny:
			return hook.Denied(verdict.Query, result)
		case err != nil:
			return blocked("The policy allows this call, but OpenPlexer could not record the decision, so it blocks the call: %v", err)
		}
		return hook.Verdict{}
	}
}

func blocked(format string, args ...any) hook.Verdict {
	return hook.Verdict{Outcome: hook.Deny, Reason: fmt.Sprintf(format, args...)}
}
