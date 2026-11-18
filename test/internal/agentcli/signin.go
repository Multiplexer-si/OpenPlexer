package agentcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

func CheckSignIn(ctx context.Context, name, bin, dir string, env []string) error {
	var args []string
	var login string
	switch name {
	case "claude":
		args, login = []string{"auth", "status"}, "claude auth login"
	case "codex":
		args, login = []string{"login", "status"}, "codex login"
	default:
		return fmt.Errorf("unknown agent %s", name)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir, cmd.Env = dir, env
	var err error
	if name == "claude" {
		out, runErr := cmd.Output()
		var status struct {
			LoggedIn bool `json:"loggedIn"`
		}
		err = errors.Join(runErr, json.Unmarshal(out, &status))
		if !status.LoggedIn && err == nil {
			err = errors.New("no sign-in reported")
		}
	} else {
		err = cmd.Run()
	}
	if err != nil {
		return fmt.Errorf("%s %s failed (%w). Run `%s` in a plain terminal", name, strings.Join(args, " "), errors.Join(err, ctx.Err()), login)
	}
	return nil
}
