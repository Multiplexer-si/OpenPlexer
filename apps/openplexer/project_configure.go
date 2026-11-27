package main

import (
	"os"

	"github.com/multiplexer-si/openplexer/internal/daemon"
	"github.com/multiplexer-si/openplexer/internal/projects"
)

func (c command) configureProject(root string, change daemon.Settings, configuring bool) error {
	user, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if !configuring {
		return projects.Register(user, root, c.home, daemon.ValidateConfiguredHome)
	}
	return projects.RegisterConfigured(user, root, c.home, func(home string, publish func() error) error {
		return daemon.ConfigureAndRegister(home, change, publish)
	})
}
