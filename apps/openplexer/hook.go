package main

import (
	"context"
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

func (c command) runHook(args []string) int {
	if len(args) >= 2 && (args[1] == hook.PreTool || args[1] == hook.SessionStart) {
		agent, ok := adapter.Lookup(args[0])
		portable := len(args) == 3 && args[2] == "--project-home"
		if ok && (len(args) == 2 || portable) {
			if !portable {
				if c.home == "" {
					c.home = os.Getenv("OPENPLEXER_HOME")
				}
				if c.home == "" {
					user, err := os.UserHomeDir()
					if err != nil {
						return c.fail(err)
					}
					c.home = filepath.Join(user, ".openplexer")
				}
			}
			return c.hook(agent, args[1], portable)
		}
	}
	fmt.Fprint(c.stderr, usage)
	return exitUsage
}

func (c command) hook(agent hook.Agent, event string, portable bool) int {
	watchdog := time.AfterFunc(hookBudget, func() {
		fmt.Fprintf(c.stderr, "openplexer: the hook did not answer within %s, so it blocks this call\n", hookBudget)
		abort()
	})
	defer watchdog.Stop()

	userHome, _ := os.UserHomeDir()
	if event == hook.SessionStart {
		input, err := io.ReadAll(io.LimitReader(c.stdin, 1<<20))
		if err != nil {
			return exitOK
		}
		ctx, cancel := context.WithTimeout(context.Background(), daemon.RefreshBudget)
		defer cancel()
		return c.sessionStart(portable, userHome, input, ctx)
	}
	input, err := io.ReadAll(c.stdin)
	if err != nil {
		return c.emit(hook.Render(hook.Verdict{Outcome: hook.Refuse, Reason: "cannot read the hook input: " + err.Error()}))
	}
	decisionCtx, stopDecision := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopDecision()
	refreshCtx, stopRefresh := context.WithTimeout(decisionCtx, daemon.RefreshBudget)
	defer stopRefresh()
	if portable {
		return c.emit(agent.HandleProject(input, func(call hook.Call, translate hook.Translate) hook.Verdict {
			return c.checkProjectCall(refreshCtx, decisionCtx, agent.Name, userHome, call, translate)
		}))
	}
	var gate *daemon.Gate
	env := hook.Env{
		Root:   c.projectRoot(agent.Name, userHome),
		Guards: c.guards(userHome),
		Home:   userHome,
		Prepare: func() (bool, bool, error) {
			var err error
			gate, err = c.openForHook(refreshCtx)
			if err != nil {
				return false, false, c.hookPolicyError(err)
			}
			status := gate.Status()
			return !status.Enforced() || gate.ExecGated(), status.HubNeedsRefresh(), nil
		},
	}
	return c.emit(agent.Handle(input, env, func(first policy.Query, rest ...policy.Query) hook.Verdict {
		return c.judge(decisionCtx, agent.Name, gate, hook.Denied)(first, rest...)
	}))
}

func (c command) journal() hook.Journal {
	return hook.Journal{Dir: filepath.Join(c.home, "installs")}
}

func (c command) projectRoot(agent, userHome string) func(cwd string) (string, error) {
	return func(cwd string) (string, error) {
		recorded, err := c.journal().Roots()
		if err != nil {
			return "", fmt.Errorf("the record of the installed projects cannot be read: %w. Make it readable or delete it yourself, then run `openplexer install %s --project <dir>` again for a project whose record you deleted", err, agent)
		}
		root, err := hook.Roots{Home: userHome, Recorded: recorded}.Find(cwd)
		if err != nil {
			return "", fmt.Errorf("%w. Run the agent inside the project, or record the project root with `openplexer install %s --project <dir>`", err, agent)
		}
		return root, nil
	}
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
	return append([]hook.Guard{{Path: home, Why: homeGuardReason}}, agentGuards(userHome)...)
}

func agentGuards(userHome string) []hook.Guard {
	var guards []hook.Guard
	for _, agent := range adapter.Agents {
		guards = append(guards, agent.Guards(agent.UserDir(os.Getenv, userHome))...)
	}
	return guards
}

func (c command) hookPolicyError(err error) error {
	return fmt.Errorf("OpenPlexer blocked this call because it cannot read its configuration or refresh its policy in %s. %w. Run `openplexer policy status` to inspect the home.", c.home, err)
}

func (c command) judge(ctx context.Context, agent string, gate *daemon.Gate, deny func(policy.Query, policy.Result) hook.Verdict) hook.Judge {
	return func(first policy.Query, rest ...policy.Query) hook.Verdict {
		verdict, err := gate.DecideContext(ctx, agent, first, rest...)
		result, decided := verdict.Result()
		switch {
		case !decided && verdict.Policy.State == daemon.Absent:
			return blocked("OpenPlexer has no policy loaded, so it blocks this call. Load a signed policy with `openplexer policy load <file>`, or remove the hook with `openplexer uninstall %s`.", agent)
		case !decided && verdict.Policy.MaxPolicyAge != 0:
			return blocked("OpenPlexer cannot verify its policy, so it blocks this call: %s. Run `openplexer policy sync` for home %q.", verdict.Policy.Detail, c.home)
		case !decided:
			return blocked("OpenPlexer cannot verify its policy, so it blocks this call: %s. Run `openplexer policy status`, then load a policy that verifies.", verdict.Policy.Detail)
		case result.Decision == policy.Deny:
			if err := gate.QueueDenial(verdict, agent); err != nil {
				fmt.Fprintf(c.stderr, "openplexer: cannot queue Room denial: %v\n", err)
			}
			if result.Rule == daemon.StalePolicyReason {
				return blocked("OpenPlexer: %s (home %q).", result.Rule, c.home)
			}
			return deny(verdict.Query, result)
		case err != nil:
			return blocked("The policy allows this call, but OpenPlexer could not record the decision, so it blocks the call: %v", err)
		}
		return hook.Verdict{}
	}
}

func blocked(format string, args ...any) hook.Verdict {
	return hook.Verdict{Outcome: hook.Deny, Reason: fmt.Sprintf(format, args...)}
}
