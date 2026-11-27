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
	"github.com/multiplexer-si/openplexer/internal/projects"
)

type hookFile struct {
	agent    hook.Agent
	path     string
	root     string
	home     string
	portable bool
}

func (c command) hookFile(name string, args []string) (hookFile, bool) {
	flags := c.flags(name)
	scope := flags.String("scope", "", "")
	project := flags.String("project", "", "")
	file := flags.String("file", "", "")
	home := flags.String("home", c.home, "")
	var portable bool
	if name == "install" {
		flags.BoolVar(&portable, "portable", false, "")
	}
	if len(args) == 0 || flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
		return hookFile{}, false
	}
	agent, ok := adapter.Lookup(args[0])
	if !ok {
		return hookFile{}, false
	}
	if *scope == "" {
		*scope = "user"
		if *project != "" {
			*scope = "project"
		}
	}
	var path, root string
	var err error
	switch {
	case portable && (*scope != "project" || *file != ""):
		err = errors.New("--portable requires a project hook file")
	case *file != "" && *project == "":
		path, err = filepath.Abs(*file)
	case *file != "":
		err = errors.New("--file names the hook file itself, so it does not go with --project")
	case *scope == "user" && *project != "":
		err = errors.New("--project names a project, so it does not go with --scope user")
	case *scope == "user":
		userHome, _ := os.UserHomeDir()
		if path = agent.UserFile(agent.UserDir(os.Getenv, userHome)); !filepath.IsAbs(path) {
			err = errors.New("the home directory is not known, so the user hook file is not known")
		}
	case *scope == "project":
		if portable {
			dir := *project
			if dir == "" {
				dir = "."
			}
			root, _, err = projects.Directory(dir)
		} else {
			root, err = filepath.Abs(*project)
		}
		if err == nil {
			path = agent.ProjectFile(root)
		}
	default:
		err = fmt.Errorf("the scope %q is not user or project", *scope)
	}
	if err != nil {
		fmt.Fprintf(c.stderr, "openplexer: %v\n", err)
		return hookFile{}, false
	}
	return hookFile{agent, path, root, *home, portable}, true
}

var executablePath = os.Executable

func (c command) install(args []string) int {
	file, ok := c.hookFile("install", args)
	if !ok {
		fmt.Fprint(c.stderr, usage)
		return exitUsage
	}
	var err error
	if c.home, err = projects.ResolveHome(file.home); err != nil {
		return c.fail(err)
	}
	agent, path, root := file.agent, file.path, file.root
	executable := "openplexer"
	if file.portable {
		if err := c.registerProject(root, nil); err != nil {
			return c.fail(err)
		}
	} else {
		executable, err = executablePath()
	}
	if err != nil {
		return c.fail(fmt.Errorf("the path of this binary is not known, so the hook cannot name it: %w", err))
	}
	handler := agent.Handler(executable)
	recordedRoot := root
	if file.portable {
		handler.Command += " --project-home"
		recordedRoot = ""
	}
	change, err := c.journal().Install(path, handler, recordedRoot)
	if err != nil {
		return c.fail(err)
	}
	switch change {
	case hook.Added:
		fmt.Fprintf(c.stdout, "installed the OpenPlexer hook of %s in %s\n", agent.Name, path)
	case hook.Repointed:
		fmt.Fprintf(c.stdout, "pointed the OpenPlexer hook of %s in %s at %s\n", agent.Name, path, executable)
	default:
		fmt.Fprintf(c.stdout, "the OpenPlexer hook of %s is already in %s, so nothing changed\n", agent.Name, path)
	}
	if file.portable {
		fmt.Fprintf(c.stdout, "registered %s in the project registry\n", root)
	} else if root != "" {
		fmt.Fprintf(c.stdout, "recorded %s as the project root, so a repository inside it does not move the root\n", root)
	}
	fmt.Fprintln(c.stdout, agent.NextStep)
	if gate, err := daemon.Open(c.home, c.now); err != nil || !gate.Status().Enforced() {
		fmt.Fprintln(c.stderr, "openplexer: no verified policy is in force, so the hook blocks every shell command and every write in a project. Load one with `openplexer policy load <file>`.")
	}
	return exitOK
}

func (c command) uninstall(args []string) int {
	file, ok := c.hookFile("uninstall", args)
	if !ok {
		fmt.Fprint(c.stderr, usage)
		return exitUsage
	}
	var err error
	if c.home, err = projects.ResolveHome(file.home); err != nil {
		return c.fail(err)
	}
	agent, path := file.agent, file.path
	change, err := c.journal().Uninstall(path, agent.Name)
	if err != nil {
		return c.fail(err)
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

func (c command) registerProject(root string, validate func(string) error) error {
	user, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return projects.Register(user, root, c.home, validate)
}
