package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/multiplexer-si/openplexer/test/internal/agentcli"
)

func (r *run) newParticipant(name, vendor, model, member, bin string) participant {
	root := filepath.Join(r.dir, member)
	return participant{
		name: name, vendor: vendor, model: model, member: member, bin: bin,
		root: root, project: filepath.Join(root, "project"), home: filepath.Join(root, "openplexer"),
	}
}

func (p participant) path(parts ...string) string {
	return filepath.Join(append([]string{p.root}, parts...)...)
}

func (p participant) identity(session string) map[string]string {
	return map[string]string{"id": session, "vendor": p.vendor}
}

// A non-nil Cmd.Env starts with an empty environment, like env -i.
func serviceEnv(p participant) []string {
	return []string{
		"HOME=" + p.path("home"), "PATH=" + os.Getenv("PATH"), "SHELL=/bin/sh",
		"TMPDIR=" + p.path("tmp"), "XDG_CONFIG_HOME=" + p.path("home", ".config"),
		"XDG_CACHE_HOME=" + p.path("home", ".cache"), "XDG_DATA_HOME=" + p.path("home", ".local", "share"),
		"XDG_STATE_HOME=" + p.path("home", ".local", "state"),
		"OPENPLEXER_HOME=" + p.home, "NO_PROXY=127.0.0.1,localhost",
	}
}

func (p *participant) prepare() error {
	for _, dir := range []string{p.path("home"), p.path("tmp"), p.path("state"),
		filepath.Join(p.project, "allowed"), filepath.Join(p.project, "sentinel")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	p.env = []string{
		"HOME=" + os.Getenv("HOME"), "USER=" + os.Getenv("USER"), "PATH=" + os.Getenv("PATH"),
		"SHELL=" + os.Getenv("SHELL"), "TMPDIR=" + os.Getenv("TMPDIR"),
		"OPENPLEXER_HOME=" + p.home,
	}
	if p.name == "claude" {
		p.env = agentcli.ClaudeEnv(p.env, p.path("state"))
	}
	return nil
}

func (p participant) codexHooks() ([]string, error) {
	return agentcli.HooksOverrides(filepath.Join(p.project, ".codex", "hooks.json"), nil)
}

func (r *run) runAgent(ctx context.Context, p participant, name, prompt string) (runErr error) {
	var args, leftovers []string
	switch p.name {
	case "claude":
		session := r.session
		if name != "claim" {
			session = agentcli.NewSessionID()
		}
		leftovers = agentcli.ClaudeLeftovers(filepath.Join(os.Getenv("HOME"), ".claude"), p.project, session)
		if err := agentcli.RequireAbsent(leftovers); err != nil {
			return err
		}
		args = append(agentcli.ClaudeArgs(session, p.model, "Write"), "--max-turns", "2")
	case "codex":
		hooks, err := p.codexHooks()
		if err != nil {
			return err
		}
		args = agentcli.CodexArgs(p.project, p.path("state"), p.model, hooks)
	default:
		return fmt.Errorf("unknown agent %s", p.name)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := child(ctx, p.project, p.env, p.bin, args...)
	cmd.Stdin = strings.NewReader(prompt)
	log := p.path(name + ".log")
	file, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	cmd.Stdout, cmd.Stderr = file, file
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s %s session: %w (log %s)", p.name, name, err, log)
	}
	defer func() {
		for _, path := range leftovers {
			runErr = errors.Join(runErr, os.RemoveAll(path))
		}
	}()
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%s %s session: %w (log %s)", p.name, name, errors.Join(err, ctx.Err()), log)
	}
	return nil
}

func checkWrite(p participant, target, content string, allowed bool) (string, error) {
	path := filepath.Join(p.project, target)
	if allowed {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(string(data)) != content {
			return "", fmt.Errorf("%s has unexpected contents", path)
		}
	} else if _, err := os.Lstat(path); !os.IsNotExist(err) {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("denied path %s exists on disk", path)
	}

	var records []struct {
		Action  string `json:"action"`
		Target  string `json:"target"`
		Session string `json:"agent_session_id"`
		Member  string `json:"actor_member_id"`
		Project string `json:"workspace_id"`
		Meta    struct {
			Agent   string `json:"agent"`
			Action  string `json:"action"`
			State   string `json:"policy_state"`
			Version int    `json:"policy_version"`
		} `json:"meta"`
	}
	log := filepath.Join(p.home, "audit.jsonl")
	if err := readLines(log, &records); err != nil {
		return "", err
	}
	action := "policy.denied"
	if allowed {
		action = "policy.allowed"
	}
	var session string
	for _, record := range records {
		if record.Target != target {
			continue
		}
		m := record.Meta
		if record.Action != action || m.Agent != p.vendor || m.Action != "write" || m.State != "verified" ||
			m.Version != 1 || record.Member != p.member || record.Project != projectID || record.Session == "" {
			return "", fmt.Errorf("%s has an unexpected hook audit record for %s", log, target)
		}
		session = record.Session
	}
	if session == "" {
		return "", fmt.Errorf("%s has no %s hook audit record for %s", log, action, target)
	}
	return session, nil
}

func executable(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	return filepath.Abs(path)
}
