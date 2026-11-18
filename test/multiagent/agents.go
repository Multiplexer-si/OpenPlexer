package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
		"CLAUDE_CONFIG_DIR=" + p.path("home", ".claude"), "CODEX_HOME=" + p.path("home", ".codex"),
		"OPENPLEXER_HOME=" + p.home, "NO_PROXY=127.0.0.1,localhost",
	}
}

func (p *participant) prepare() error {
	for _, dir := range []string{p.path("home", ".claude"), p.path("home", ".codex"), p.path("tmp"),
		p.path("state"), filepath.Join(p.project, "allowed"), filepath.Join(p.project, "sentinel")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	p.env = serviceEnv(*p)
	if p.name == "claude" {
		p.env = append(p.env, "CLAUDE_CODE_TMPDIR="+p.path("tmp"), "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1")
	}
	return nil
}

func (p *participant) authenticate(ctx context.Context) error {
	if p.name == "claude" {
		for _, key := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
			if value := os.Getenv(key); value != "" {
				p.env = append(p.env, key+"="+value)
				return nil
			}
		}
		return errors.New("set ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN for the isolated Claude Code home")
	}

	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		input := []byte(key + "\n")
		_, err := runCommand(ctx, p.project, p.env, p.bin, input, "-c", `cli_auth_credentials_store="file"`, "login", "--with-api-key")
		if err != nil {
			return errors.New("Codex login --with-api-key failed in the isolated home")
		}
		return nil
	}
	source := os.Getenv("MULTIAGENT_CODEX_AUTH_FILE")
	if source == "" {
		return errors.New("set OPENAI_API_KEY or MULTIAGENT_CODEX_AUTH_FILE for the isolated Codex home")
	}
	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return err
	}
	for _, dir := range []string{filepath.Join(home, ".codex"), filepath.Join(home, ".claude")} {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			dir = resolved
		}
		if source == dir || strings.HasPrefix(source, dir+string(os.PathSeparator)) {
			return errors.New("MULTIAGENT_CODEX_AUTH_FILE must be outside the default agent homes")
		}
	}
	auth, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(p.path("home", ".codex", "auth.json"), auth, 0o600)
}

func (p participant) seedClaudeSettings() error {
	const settings = `{
		"sandbox": {
			"enabled": true,
			"failIfUnavailable": true,
			"autoAllowBashIfSandboxed": true,
			"allowUnsandboxedCommands": false,
			"network": {"allowedDomains": []}
		}
	}`
	dir := filepath.Join(p.project, ".claude")
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o600)
}

// Codex accepts the installed hook as a session setting, as in test/realagent.
func (p participant) codexHooks() (string, error) {
	var file struct {
		Hooks struct {
			PreToolUse []struct {
				Matcher string
				Hooks   []struct {
					Type, Command string
					Timeout       int
				}
			}
		}
	}
	path := filepath.Join(p.project, ".codex", "hooks.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return "", fmt.Errorf("decode installed Codex hook: %w", err)
	}
	groups := file.Hooks.PreToolUse
	if len(groups) != 1 || len(groups[0].Hooks) != 1 {
		return "", errors.New("expected one installed Codex PreToolUse hook")
	}
	h := groups[0].Hooks[0]
	return fmt.Sprintf("hooks.PreToolUse=[{matcher=%q,hooks=[{type=%q,command=%q,timeout=%d}]}]",
		groups[0].Matcher, h.Type, h.Command, h.Timeout), nil
}

func newSessionID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func (r *run) runAgent(ctx context.Context, p participant, name, prompt string) error {
	var args []string
	switch p.name {
	case "claude":
		session := r.session
		if name != "claim" {
			session = newSessionID()
		}
		args = []string{"-p", "--session-id", session, "--model", p.model, "--effort", "low",
			"--max-turns", "2", "--setting-sources", "project", "--tools", "Write",
			"--permission-mode", "acceptEdits", "--permission-prompts", "none", "--strict-mcp-config",
			"--disable-slash-commands", "--no-session-persistence",
			"--output-format", "stream-json", "--verbose", "--include-hook-events"}
	case "codex":
		hooks, err := p.codexHooks()
		if err != nil {
			return err
		}
		args = []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check",
			"--ignore-user-config", "--ignore-rules", "--dangerously-bypass-hook-trust",
			"--disable", "plugins", "--disable", "remote_plugin", "--disable", "apps", "--disable", "shell_snapshot",
			"-m", p.model, "-c", `model_reasoning_effort="low"`, "-s", "workspace-write",
			"-c", "sandbox_workspace_write.network_access=false",
			"-c", "sandbox_workspace_write.exclude_tmpdir_env_var=true",
			"-c", "sandbox_workspace_write.exclude_slash_tmp=true",
			"-c", `approval_policy="never"`, "-c", `cli_auth_credentials_store="file"`,
			"-c", "sqlite_home=" + strconv.Quote(p.path("state")),
			"-c", "log_dir=" + strconv.Quote(p.path("state", "log")),
			"-c", hooks, "-C", p.project}
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
	if err := cmd.Run(); err != nil {
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
