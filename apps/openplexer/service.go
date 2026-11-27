package main

import (
	"fmt"

	"github.com/multiplexer-si/openplexer/internal/daemon"
	"github.com/multiplexer-si/openplexer/internal/projects"
)

func (c command) service(args []string) int {
	if len(args) == 0 || (args[0] != "install" && args[0] != "uninstall" && args[0] != "status") {
		fmt.Fprint(c.stderr, usage)
		return exitUsage
	}
	flags := c.flags("service " + args[0])
	explicit := flags.String("home", c.home, "")
	if err := flags.Parse(args[1:]); err != nil {
		return exitUsage
	}
	if flags.NArg() != 0 {
		flags.Usage()
		return exitUsage
	}
	home, err := projects.ResolveHome(*explicit)
	if err != nil {
		return c.fail(err)
	}
	s, err := daemon.NewService(home)
	if err != nil {
		return c.fail(err)
	}
	c.home = s.Home
	switch args[0] {
	case "install":
		binary, err := executablePath()
		if err != nil {
			return c.fail(err)
		}
		if err := s.Install(binary); err != nil {
			return c.fail(err)
		}
		fmt.Fprintf(c.stdout, "installed service %s for %s\nlogs: %s\n", s.Name, c.home, s.LogLocation())
	case "uninstall":
		if err := s.Uninstall(); err != nil {
			return c.fail(err)
		}
		fmt.Fprintf(c.stdout, "uninstalled service %s for %s\n", s.Name, c.home)
	case "status":
		return c.serviceStatus(s)
	}
	return exitOK
}

func (c command) serviceStatus(s *daemon.Service) int {
	status, managerErr := s.Status()
	if managerErr != nil {
		status.State = "failed"
		status.Failure = managerErr.Error()
	}
	fmt.Fprintf(c.stdout, "service: %s\nname: %s\n", status.State, s.Name)
	if status.PID > 0 {
		fmt.Fprintf(c.stdout, "pid: %d\n", status.PID)
	}
	if status.Failure != "" {
		fmt.Fprintf(c.stdout, "manager: %s\n", status.Failure)
	}
	fmt.Fprintf(c.stdout, "logs: %s\n", s.LogLocation())
	gate, err := daemon.Open(c.home, c.now)
	if err != nil {
		fmt.Fprintf(c.stdout, "policy: unavailable\nhome: %s\n", c.home)
		return c.fail(err)
	}
	c.report(gate.Status())
	if managerErr != nil {
		return exitFailure
	}
	return exitOK
}
