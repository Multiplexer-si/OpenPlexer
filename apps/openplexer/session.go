package main

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"path/filepath"

	"github.com/multiplexer-si/openplexer/internal/adapter/hook"
	"github.com/multiplexer-si/openplexer/internal/daemon"
	"github.com/multiplexer-si/openplexer/internal/projects"
)

func (c command) checkSession() int {
	ctx, cancel := context.WithTimeout(context.Background(), daemon.RefreshBudget)
	defer cancel()
	gate, err := c.openForHook(ctx)
	if err != nil {
		return c.fail(err)
	}
	c.report(gate.Status())
	return policyStatusExit(gate.Status())
}

func (c command) openForHook(ctx context.Context) (*daemon.Gate, error) {
	gate, err := daemon.Open(c.home, c.now)
	if err != nil {
		return nil, err
	}
	return gate, gate.RefreshIfStale(ctx)
}

func (c command) sessionStart(portable bool, userHome string, input []byte, ctx context.Context) int {
	if !jsontext.Value(input).IsValid() {
		return exitOK
	}
	call, err := hook.ParseCall(input)
	if err != nil || call.Event != "SessionStart" {
		return exitOK
	}
	warn := func(reason string) int {
		text, _ := json.Marshal(map[string]any{
			"systemMessage": reason,
			"hookSpecificOutput": map[string]string{
				"hookEventName": "SessionStart", "additionalContext": reason,
			},
		})
		fmt.Fprintln(c.stdout, string(text))
		return exitOK
	}
	if portable {
		if !filepath.IsAbs(call.Cwd) {
			return warn("OpenPlexer could not resolve the session's checkout. Run `openplexer policy check-session` from the checkout.")
		}
		cwd, _, err := projects.Directory(call.Cwd)
		if err != nil {
			return warn(projectBlock(err).Reason)
		}
		registry, err := projects.Read(userHome)
		if err != nil {
			return warn(projectBlock(err).Reason)
		}
		project, err := registry.Lookup(cwd)
		if err != nil {
			return warn(projectBlock(err).Reason)
		}
		if err := project.CheckHome(); err != nil {
			return warn(projectBlock(err).Reason)
		}
		c.home = project.Home
	}
	gate, err := c.openForHook(ctx)
	if err != nil {
		return warn("OpenPlexer could not read policy settings. Run `openplexer policy status`.")
	}
	if gate.HubStale() {
		return warn("OpenPlexer: " + daemon.StalePolicyReason + ".")
	}
	if gate.Status().MaxPolicyAge != 0 && !gate.Status().Enforced() {
		return warn("OpenPlexer has no verified hub policy. Run `openplexer policy status` and `openplexer policy sync`.")
	}
	return exitOK
}
