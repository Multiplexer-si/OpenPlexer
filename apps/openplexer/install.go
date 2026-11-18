package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/multiplexer-si/openplexer/internal/adapter"
	"github.com/multiplexer-si/openplexer/internal/adapter/hook"
	"github.com/multiplexer-si/openplexer/internal/daemon"
)

func (c command) hookFile(name string, args []string) (hook.Agent, string, bool) {
	flags := c.flags(name)
	scope := flags.String("scope", string(hook.User), "")
	project := flags.String("project", "", "")
	file := flags.String("file", "", "")
	if len(args) == 0 || flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
		return hook.Agent{}, "", false
	}
	agent, ok := adapter.Lookup(args[0])
	if !ok || (*project != "" && hook.Scope(*scope) != hook.Project) {
		return hook.Agent{}, "", false
	}
	if *file != "" {
		path, err := filepath.Abs(*file)
		return agent, path, err == nil
	}
	userHome, _ := os.UserHomeDir()
	userDir := agent.UserDir(os.Getenv, userHome)
	switch hook.Scope(*scope) {
	case hook.User:
		if !filepath.IsAbs(userDir) {
			return hook.Agent{}, "", false
		}
		return agent, agent.File(hook.User, userDir, ""), true
	case hook.Project:
		dir, err := filepath.Abs(*project)
		return agent, agent.File(hook.Project, "", dir), err == nil
	}
	return hook.Agent{}, "", false
}

func (c command) install(args []string) int {
	agent, path, ok := c.hookFile("install", args)
	if !ok {
		fmt.Fprint(c.stderr, usage)
		return exitUsage
	}
	executable, err := os.Executable()
	if err != nil {
		return c.fail(fmt.Errorf("the path of this binary is not known, so the hook cannot name it: %w", err))
	}
	handler := agent.Handler(executable)
	change, err := hook.InstallFile(path, handler)
	if err != nil {
		return c.fail(fmt.Errorf("%w. The file is unchanged", err))
	}
	switch change {
	case hook.Added:
		fmt.Fprintf(c.stdout, "installed the OpenPlexer hook of %s in %s\n", agent.Name, path)
	case hook.Repointed:
		fmt.Fprintf(c.stdout, "pointed the OpenPlexer hook of %s in %s at %s\n", agent.Name, path, executable)
	default:
		fmt.Fprintf(c.stdout, "the OpenPlexer hook of %s is already in %s, so nothing changed\n", agent.Name, path)
	}
	fmt.Fprintln(c.stdout, agent.NextStep)
	if gate, err := daemon.Open(c.home, c.now); err != nil || !gate.Status().Enforced() {
		fmt.Fprintln(c.stderr, "openplexer: no verified policy is in force, so the hook blocks every shell command and every write in a project. Load one with `openplexer policy load <file>`.")
	}
	return exitOK
}

func (c command) uninstall(args []string) int {
	agent, path, ok := c.hookFile("uninstall", args)
	if !ok {
		fmt.Fprint(c.stderr, usage)
		return exitUsage
	}
	change, err := hook.UninstallFile(path, agent.Name)
	if err != nil {
		return c.fail(fmt.Errorf("%w. The file is unchanged", err))
	}
	switch _, err := os.Stat(path); {
	case change == hook.Unchanged:
		fmt.Fprintf(c.stdout, "no OpenPlexer hook of %s is in %s, so nothing changed\n", agent.Name, path)
	case errors.Is(err, fs.ErrNotExist):
		fmt.Fprintf(c.stdout, "removed the OpenPlexer hook of %s, and deleted %s, which held nothing else\n", agent.Name, path)
	default:
		fmt.Fprintf(c.stdout, "removed the OpenPlexer hook of %s from %s\n", agent.Name, path)
	}
	return exitOK
}
