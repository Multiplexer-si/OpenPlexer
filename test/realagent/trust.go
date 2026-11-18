package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type trustReport struct {
	UntrustedProject int    `json:"untrustedProject"`
	Before           string `json:"before"`
	After            string `json:"after"`
	SessionFlags     string `json:"sessionFlags"`
	Hash             string `json:"hash"`
	SameHash         bool   `json:"sameHash"`
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

	hooks, err := s.hooks(project)
	if err != nil {
		return report, err
	}
	report.UntrustedProject = len(hooks)
	if err := s.write("projects", map[string]any{project: map[string]string{"trust_level": "trusted"}}); err != nil {
		return report, err
	}
	if hooks, err = s.hooks(project); err != nil {
		return report, err
	}
	if len(hooks) != 1 {
		return report, fmt.Errorf("a trusted project lists %d hooks, want 1", len(hooks))
	}
	report.Before, report.Hash = hooks[0].TrustStatus, hooks[0].CurrentHash
	if hooks[0].Source != "project" {
		return report, fmt.Errorf("the project hook has source %q, want project", hooks[0].Source)
	}
	if err := s.write("hooks.state", map[string]any{hooks[0].Key: map[string]string{"trusted_hash": hooks[0].CurrentHash}}); err != nil {
		return report, err
	}
	if hooks, err = s.hooks(project); err != nil {
		return report, err
	}
	if len(hooks) != 1 {
		return report, fmt.Errorf("after trusting, %d hooks are listed, want 1", len(hooks))
	}
	report.After = hooks[0].TrustStatus

	flagged, err := startAppServer(ctx, project, filepath.Join(codexHome, "flags"), sessionFlags)
	if err != nil {
		return report, err
	}
	defer flagged.close()
	if hooks, err = flagged.hooks(project); err != nil {
		return report, err
	}
	if len(hooks) != 1 || hooks[0].Source != "sessionFlags" {
		return report, fmt.Errorf("the session flags list %d hooks %+v, want one with source sessionFlags", len(hooks), hooks)
	}
	report.SessionFlags = hooks[0].TrustStatus
	report.SameHash = hooks[0].CurrentHash == report.Hash

	var problems []error
	if report.UntrustedProject != 0 {
		problems = append(problems, fmt.Errorf("an untrusted project lists %d hooks, want 0", report.UntrustedProject))
	}
	if report.Before != "untrusted" || report.After != "trusted" {
		problems = append(problems, fmt.Errorf("trust status went %q then %q, want untrusted then trusted", report.Before, report.After))
	}
	if !report.SameHash {
		problems = append(problems, errors.New("the session flag hook has another hash than the project hook"))
	}
	return report, errors.Join(problems...)
}
