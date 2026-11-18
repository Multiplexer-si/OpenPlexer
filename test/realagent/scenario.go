package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

type policySetup int

const (
	verifiedPolicy policySetup = iota
	stalePolicy
	noPolicy
)

type auditWant struct {
	action string
	query  string
	target string
	state  string
}

type scenario struct {
	name        string
	prompt      string
	claudeTools string
	policy      policySetup
	offline     bool
	target      string
	wantOnDisk  bool
	want        auditWant
	reason      string
}

const (
	writePrompt  = "Use your file editing tool, not the shell, to create a file named allowed.txt in the current directory that contains the single word ok. Then reply with the word done."
	refusalNote  = " If a tool call is refused, reply with the refusal reason word for word and stop. Do not try another way."
	denyPrompt   = "Use your file editing tool, not the shell, to create a file at sentinel/blocked.txt that contains the single word ok." + refusalNote
	writeReason  = "OpenPlexer policy denied a write to sentinel/blocked.txt"
	shellCommand = "touch shell-denied.marker"
	shellReason  = `OpenPlexer policy denied the command "` + shellCommand + `"`
	absentReason = "OpenPlexer has no policy loaded"
)

var deniedWrite = auditWant{"policy.denied", "write", "sentinel/blocked.txt", "verified"}

var scenarios = []scenario{
	{
		name: "allow_write", prompt: writePrompt, claudeTools: "Write",
		target: "project/allowed.txt", wantOnDisk: true,
		want: auditWant{"policy.allowed", "write", "allowed.txt", "verified"},
	},
	{
		name: "deny_write", prompt: denyPrompt, claudeTools: "Write",
		target: "project/sentinel/blocked.txt",
		want:   deniedWrite, reason: writeReason,
	},
	{
		name:   "deny_shell",
		prompt: "Run exactly this shell command and nothing else: " + shellCommand + refusalNote, claudeTools: "Bash",
		target: "project/shell-denied.marker",
		want:   auditWant{"policy.denied", "exec", commandTarget(shellCommand), "verified"}, reason: shellReason,
	},
	{
		name: "stale", prompt: denyPrompt, claudeTools: "Write", policy: stalePolicy,
		target: "project/sentinel/blocked.txt",
		want:   auditWant{"policy.denied", "write", "sentinel/blocked.txt", "stale"}, reason: writeReason,
	},
	{
		name: "no_policy", prompt: writePrompt, claudeTools: "Write", policy: noPolicy,
		target: "project/allowed.txt",
		want:   auditWant{"policy.unenforced", "write", "allowed.txt", "absent"}, reason: absentReason,
	},
	{
		name: "offline", prompt: denyPrompt, claudeTools: "Write", offline: true,
		target: "project/sentinel/blocked.txt",
		want:   deniedWrite, reason: writeReason,
	},
}

func (s scenario) exists(root string) (bool, error) {
	_, err := os.Stat(filepath.Join(root, s.target))
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func commandTarget(command string) string {
	sum := sha256.Sum256([]byte(command))
	return "sha256:" + hex.EncodeToString(sum[:])
}
