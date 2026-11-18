package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"

	"github.com/multiplexer-si/openplexer/internal/jsonedit"
)

const offlineProfile = "(version 1)(allow default)(deny network*)"

func offlineCommand(cmd string) string {
	return "/usr/bin/sandbox-exec -p " + shellQuote(offlineProfile) + " " + cmd
}

func offlineClaudeSettings(settings []byte) ([]byte, error) {
	doc, err := jsonedit.Parse(settings)
	if err != nil {
		return nil, err
	}
	var commands []jsonedit.Node
	hooks, _, err := doc.Root().Get("hooks")
	if err != nil {
		return nil, err
	}
	preToolUse, _, err := hooks.Get("PreToolUse")
	if err != nil {
		return nil, err
	}
	for _, group := range preToolUse.Elems {
		handlers, _, err := group.Get("hooks")
		if err != nil {
			return nil, err
		}
		for _, handler := range handlers.Elems {
			command, ok, err := handler.Get("command")
			if err != nil {
				return nil, err
			}
			var text string
			if ok && doc.Decode(command, &text) == nil && strings.Contains(text, " hook claude-code pre-tool") {
				commands = append(commands, command)
			}
		}
	}
	if len(commands) != 1 {
		return nil, fmt.Errorf("settings.json holds %d installed claude-code hook commands, want 1", len(commands))
	}
	var command string
	if err := doc.Decode(commands[0], &command); err != nil {
		return nil, err
	}
	edit, err := doc.ReplaceValue(commands[0], offlineCommand(command))
	if err != nil {
		return nil, err
	}
	return doc.Apply(edit)
}

func networkControl() (plainExit, sandboxedExit int, blocked bool, err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, 0, false, err
	}
	defer ln.Close()
	port := fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)
	exit := func(name string, args ...string) (int, error) {
		err := exec.Command(name, args...).Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return 0, err
	}
	if plainExit, err = exit("/usr/bin/nc", "-z", "127.0.0.1", port); err != nil {
		return 0, 0, false, err
	}
	if sandboxedExit, err = exit("/usr/bin/sandbox-exec", "-p", offlineProfile, "/usr/bin/nc", "-z", "127.0.0.1", port); err != nil {
		return 0, 0, false, err
	}
	return plainExit, sandboxedExit, plainExit == 0 && sandboxedExit != 0, nil
}

func rewriteClaudeSettings(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	data, err = offlineClaudeSettings(data)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
