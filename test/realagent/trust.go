package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type trustReport struct {
	UntrustedProject int                  `json:"untrustedProject"`
	Hooks            map[string]hookTrust `json:"hooks"`
}

type hookTrust struct {
	Before       string `json:"before"`
	After        string `json:"after"`
	SessionFlags string `json:"sessionFlags"`
	Hash         string `json:"hash"`
	SameHash     bool   `json:"sameHash"`
}

type hookInfo struct {
	Key         string `json:"key"`
	CurrentHash string `json:"currentHash"`
	TrustStatus string `json:"trustStatus"`
	Source      string `json:"source"`
}

type appServer struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  *bufio.Scanner
	nextID int
	stderr bytes.Buffer
}

var appServerArgs = []string{"app-server",
	"--disable", "plugins", "--disable", "remote_plugin", "--disable", "apps", "--disable", "shell_snapshot"}

func startAppServer(ctx context.Context, project, codexHome string, flags []string) (*appServer, error) {
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		return nil, err
	}
	s := &appServer{}
	s.cmd = exec.CommandContext(ctx, "codex", append(append([]string{}, appServerArgs...), flags...)...)
	s.cmd.Dir = project
	s.cmd.Env = append(os.Environ(), "CODEX_HOME="+codexHome)
	s.cmd.Stderr = &s.stderr
	s.cmd.WaitDelay = 5 * time.Second
	stdout, err := s.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if s.stdin, err = s.cmd.StdinPipe(); err != nil {
		return nil, err
	}
	if err := s.cmd.Start(); err != nil {
		return nil, err
	}
	s.lines = bufio.NewScanner(stdout)
	s.lines.Buffer(nil, 16<<20)
	if _, err := s.call("initialize", map[string]any{"clientInfo": map[string]string{"name": "realagent", "version": "0"}}); err != nil {
		s.close()
		return nil, err
	}
	if _, err := fmt.Fprintln(s.stdin, `{"method":"initialized"}`); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func (s *appServer) call(method string, params any) (json.RawMessage, error) {
	s.nextID++
	request, err := json.Marshal(map[string]any{"id": s.nextID, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(s.stdin, "%s\n", request); err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	for s.lines.Scan() {
		var reply struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(s.lines.Bytes(), &reply) != nil || reply.ID == nil || reply.Method != "" || *reply.ID != s.nextID {
			continue
		}
		if len(reply.Error) > 0 {
			return nil, fmt.Errorf("%s: %s", method, reply.Error)
		}
		return reply.Result, nil
	}
	return nil, fmt.Errorf("%s: app-server closed its output: %v %s", method, s.lines.Err(), strings.TrimSpace(s.stderr.String()))
}

func (s *appServer) hooks(project string) ([]hookInfo, error) {
	raw, err := s.call("hooks/list", map[string]any{"cwds": []string{project}})
	if err != nil {
		return nil, err
	}
	var reply struct {
		Data []struct {
			Hooks []hookInfo `json:"hooks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, err
	}
	var out []hookInfo
	for _, entry := range reply.Data {
		out = append(out, entry.Hooks...)
	}
	return out, nil
}

func (s *appServer) write(keyPath string, value any) error {
	_, err := s.call("config/batchWrite", map[string]any{
		"edits": []any{map[string]any{"keyPath": keyPath, "mergeStrategy": "upsert", "value": value}},
	})
	return err
}

func (s *appServer) close() error {
	s.stdin.Close()
	return s.cmd.Wait()
}

func codexTrust(project, codexHome string, sessionFlags []string) (trustReport, error) {
	var report trustReport
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := startAppServer(ctx, project, filepath.Join(codexHome, "project"), nil)
	if err != nil {
		return report, err
	}
	defer s.close()

	return checkCodexTrust(project, s.hooks, s.write, func() ([]hookInfo, error) {
		flagged, err := startAppServer(ctx, project, filepath.Join(codexHome, "flags"), sessionFlags)
		if err != nil {
			return nil, err
		}
		defer flagged.close()
		return flagged.hooks(project)
	})
}

func checkCodexTrust(project string, list func(string) ([]hookInfo, error), write func(string, any) error, flags func() ([]hookInfo, error)) (trustReport, error) {
	report := trustReport{Hooks: make(map[string]hookTrust)}
	hooks, err := list(project)
	if err != nil {
		return report, err
	}
	report.UntrustedProject = len(hooks)
	if report.UntrustedProject != 0 {
		return report, fmt.Errorf("an untrusted project lists %d hooks, want 0", report.UntrustedProject)
	}
	if err := write("projects", map[string]any{project: map[string]string{"trust_level": "trusted"}}); err != nil {
		return report, err
	}
	if hooks, err = list(project); err != nil {
		return report, err
	}
	before, err := installedHooks(hooks, "project", "untrusted")
	if err != nil {
		return report, err
	}
	state := make(map[string]any, len(before))
	for event, h := range before {
		report.Hooks[event] = hookTrust{Before: h.TrustStatus, Hash: h.CurrentHash}
		state[h.Key] = map[string]string{"trusted_hash": h.CurrentHash}
	}
	if err := write("hooks.state", state); err != nil {
		return report, err
	}
	if hooks, err = list(project); err != nil {
		return report, err
	}
	after, err := installedHooks(hooks, "project", "trusted")
	if err != nil {
		return report, err
	}
	for event, h := range after {
		check := report.Hooks[event]
		check.After = h.TrustStatus
		report.Hooks[event] = check
		if h.Key != before[event].Key || h.CurrentHash != check.Hash {
			return report, fmt.Errorf("%s project hook changed identity or hash after trusting", event)
		}
	}
	hooks, err = flags()
	if err != nil {
		return report, err
	}
	flagged, err := installedHooks(hooks, "sessionFlags", "")
	if err != nil {
		return report, err
	}
	for event, h := range flagged {
		check := report.Hooks[event]
		check.SessionFlags = h.TrustStatus
		check.SameHash = h.CurrentHash == check.Hash
		report.Hooks[event] = check
		if !check.SameHash {
			return report, fmt.Errorf("%s session flag hook has a different hash from the project hook", event)
		}
	}
	return report, nil
}

func installedHooks(hooks []hookInfo, source, status string) (map[string]hookInfo, error) {
	// The harness installs into an empty project, with one group and handler per event.
	events := map[string]string{"PreToolUse": ":pre_tool_use:0:0", "SessionStart": ":session_start:0:0"}
	if len(hooks) != len(events) {
		return nil, fmt.Errorf("%s lists %d hooks, want one per installed event", source, len(hooks))
	}
	byEvent := make(map[string]hookInfo, len(events))
	for _, h := range hooks {
		if h.Source != source || h.CurrentHash == "" || status != "" && h.TrustStatus != status {
			return nil, fmt.Errorf("unexpected %s hook listing: %+v", source, h)
		}
		for event, suffix := range events {
			if strings.HasSuffix(h.Key, suffix) {
				byEvent[event] = h
				break
			}
		}
	}
	for event := range events {
		if _, ok := byEvent[event]; !ok {
			return nil, fmt.Errorf("%s does not list exactly one %s handler", source, event)
		}
	}
	return byEvent, nil
}
