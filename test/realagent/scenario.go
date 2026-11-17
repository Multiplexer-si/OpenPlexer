package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type scenario struct {
	name       string
	prompt     string
	denyTokens []string
	reason     string
	target     string
	wantOnDisk bool
}

const (
	writeReason = "STUBDENY-WRITE: this path is reserved by policy"
	shellReason = "STUBDENY-SHELL: this command is blocked by policy"
	refusalNote = " If a tool call is refused, reply with the refusal reason word for word and stop. Do not try another way."
)

var scenarios = []scenario{
	{
		name:       "allow_write",
		prompt:     "Use your file editing tool, not the shell, to create a file named allowed.txt in the current directory that contains the single word ok. Then reply with the word done.",
		target:     "allowed.txt",
		wantOnDisk: true,
	},
	{
		name:       "deny_write",
		prompt:     "Use your file editing tool, not the shell, to create a file at sentinel/blocked.txt that contains the single word ok." + refusalNote,
		denyTokens: []string{"sentinel/"},
		reason:     writeReason,
		target:     "sentinel/blocked.txt",
	},
	{
		name:       "deny_shell",
		prompt:     "Run exactly this shell command and nothing else: touch shell-denied.marker" + refusalNote,
		denyTokens: []string{"shell-denied"},
		reason:     shellReason,
		target:     "shell-denied.marker",
	},
}

func (s scenario) reasonToken() string {
	if s.reason == "" {
		return ""
	}
	token, _, _ := strings.Cut(s.reason, ":")
	return token
}

func (s scenario) exists(dir string) (bool, error) {
	_, err := os.Stat(filepath.Join(dir, s.target))
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func findScenario(name string) (scenario, error) {
	for _, s := range scenarios {
		if s.name == name {
			return s, nil
		}
	}
	return scenario{}, fmt.Errorf("unknown scenario %q", name)
}
