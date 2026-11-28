package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/multiplexer-si/openplexer/internal/adapter/hook"
	"github.com/multiplexer-si/openplexer/internal/daemon"
	"github.com/multiplexer-si/openplexer/internal/policy"
	"github.com/multiplexer-si/openplexer/internal/projects"
)

const homeGuardReason = "it holds the policy, the key set, and the audit log of OpenPlexer"

type projectQueries struct {
	home     string
	writes   []policy.Query
	commands []policy.Query
	shell    bool
}

func (c command) checkProjectCall(refreshCtx, decisionCtx context.Context, agent, userHome string, call hook.Call, translate hook.Translate) hook.Verdict {
	registry, err := projects.Read(userHome)
	if err != nil {
		return projectBlock(fmt.Errorf("cannot read the project registry: %w", err))
	}
	if !filepath.IsAbs(call.Cwd) {
		return projectBlock(fmt.Errorf("the hook input cwd must be absolute"))
	}
	cwd, _, err := projects.Directory(call.Cwd)
	if err != nil {
		return projectBlock(fmt.Errorf("cannot resolve the hook input cwd: %w", err))
	}
	lookup := func(path string) (projects.Project, error) {
		project, err := registry.Lookup(path)
		if err != nil {
			return project, fmt.Errorf("cannot select a home for %s: %w", path, err)
		}
		return project, nil
	}
	var shellProject projects.Project
	if call.Tool == "Bash" {
		shellProject, err = lookup(cwd)
		if err != nil {
			return projectBlock(err)
		}
		if err := shellProject.CheckHome(); err != nil {
			return projectBlock(err)
		}
	}
	effect, err := translate(call)
	if err != nil {
		return hook.Verdict{Outcome: hook.Refuse, Reason: err.Error()}
	}

	guards := projectGuards(registry, userHome)
	var writes []hook.ResolvedWrite
	for _, target := range effect.Targets {
		write, err := hook.ResolveWrite(target, cwd, userHome)
		if err != nil {
			return projectBlock(fmt.Errorf("cannot resolve a write target: %w", err))
		}
		for _, guard := range guards {
			if verdict, blocked := write.CheckGuard(guard.Path, guard.Why); blocked {
				return verdict
			}
		}
		writes = append(writes, write)
	}

	var groups []*projectQueries
	byHome := map[string]*projectQueries{}
	groupFor := func(project projects.Project) (*projectQueries, error) {
		group, ok := byHome[project.Home]
		if !ok {
			if project.Home != shellProject.Home {
				if err := project.CheckHome(); err != nil {
					return nil, err
				}
			}
			group = &projectQueries{home: project.Home}
			byHome[project.Home] = group
			groups = append(groups, group)
		}
		return group, nil
	}
	var session *string
	if call.Session != "" {
		session = &call.Session
	}
	for _, write := range writes {
		project, err := lookup(write.Path)
		if err != nil {
			return projectBlock(err)
		}
		group, err := groupFor(project)
		if err != nil {
			return projectBlock(err)
		}
		rel, err := filepath.Rel(project.Root, write.Path)
		if err != nil {
			return projectBlock(err)
		}
		group.writes = append(group.writes, policy.Query{Action: policy.Write, Path: filepath.ToSlash(rel), AgentSessionID: session})
	}
	if call.Tool == "Bash" {
		group, err := groupFor(shellProject)
		if err != nil {
			return projectBlock(err)
		}
		group.shell = true
		for _, name := range effect.Commands {
			group.commands = append(group.commands, policy.Query{Action: policy.Exec, Command: name, AgentSessionID: session})
		}
	}
	for _, group := range groups {
		c.home = group.home
		decision, err := daemon.OpenForHook(refreshCtx, c.home, c.now)
		if err != nil {
			return hook.Verdict{Outcome: hook.Deny, Reason: c.hookPolicyError(err).Error()}
		}
		queries := group.writes
		if decision.ExecGated() {
			queries = append(queries, group.commands...)
		}
		if len(queries) == 0 && group.shell && decision.RefreshBlocked() {
			command, _ := call.Text("command")
			queries = append(queries, policy.Query{Action: policy.Exec, Command: command, AgentSessionID: session})
		}
		if verdict := c.judge(decisionCtx, agent, decision, projectDenied, queries); verdict.Outcome != hook.Allow {
			return verdict
		}
	}
	return hook.Verdict{}
}

func projectDenied(query policy.Query, result policy.Result) hook.Verdict {
	if query.Action == policy.Exec {
		return blocked("OpenPlexer policy denied the command: %s. Ask the owner of the policy to grant it.", result.Rule)
	}
	return hook.Denied(query, result)
}

func projectBlock(err error) hook.Verdict {
	return blocked("OpenPlexer blocked this call: %v. Register the checkout with `openplexer configure --project-root <dir>` using your configured home.", err)
}

func projectGuards(registry projects.Registry, userHome string) []hook.Guard {
	var guards []hook.Guard
	add := func(path, reason string) {
		for _, resolved := range hook.ResolveGuardPaths(path) {
			guards = append(guards, hook.Guard{Path: resolved, Why: reason})
		}
	}
	add(registry.Path, "it is the OpenPlexer project registry")
	registered := registry.Projects()
	for _, project := range registered {
		add(project.Home, homeGuardReason)
	}
	for _, guard := range agentGuards(userHome) {
		if filepath.IsAbs(guard.Path) {
			add(guard.Path, guard.Why)
		} else {
			for _, project := range registered {
				add(filepath.Join(project.Root, guard.Path), guard.Why)
			}
		}
	}
	return guards
}
