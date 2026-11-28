package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/hub"
	"github.com/multiplexer-si/openplexer/internal/policy"
	"github.com/multiplexer-si/openplexer/internal/projects"
)

type joinHandlerTransport struct{ handler http.Handler }

func (tr joinHandlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	res := httptest.NewRecorder()
	tr.handler.ServeHTTP(res, req)
	return res.Result(), nil
}

func joinCLI(t *testing.T) (c cli, user, root, code string) {
	t.Helper()
	return joinCLIWithRepo(t, "team/demo", "git@github.example.test:team/demo.git")
}

func joinCLIWithRepo(t *testing.T, repo, remote string) (c cli, user, root, code string) {
	t.Helper()
	user, err := projects.CanonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", user)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(user, "config"))
	t.Setenv("OPENPLEXER_HOME", filepath.Join(user, "ignored"))
	root, err = projects.CanonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", remote}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
	}
	nested := filepath.Join(root, "src")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)

	store := hub.Open(t.TempDir())
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	if err := store.AddProject("demo", repo); err != nil {
		t.Fatal(err)
	}
	repos, err := json.Marshal([]string{repo})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishPolicy("demo", []byte(`{"project_id":"demo","repos":`+string(repos)+`,"version":1,"areas":[],"roles":{},"members":{}}`)); err != nil {
		t.Fatal(err)
	}
	invite, err := store.CreateInvite(hub.InviteInput{ProjectID: "demo", MemberID: "dev", Role: hub.Member, ExpiresIn: 60})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := store.PolicyKeys()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(keys)
	fp, err := policy.FingerprintKeySet(data)
	if err != nil {
		t.Fatal(err)
	}
	tr := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = tr })
	http.DefaultTransport = joinHandlerTransport{store.Handler()}
	return cli{t: t, now: loadedAt, stdin: "https://hub.example.test/join/" + invite.Code + "#fp=" + fp + "\n"}, user, root, invite.Code
}

func TestJoinCommandViaStdin(t *testing.T) {
	c, user, root, code := joinCLI(t)
	status, out, stderr := c.run("join", "--link-stdin")
	if strings.Contains(out+stderr, code) || strings.Contains(out+stderr, strings.TrimSpace(c.stdin)) {
		t.Fatal("join exposed the invite")
	}
	if status != exitOK {
		t.Fatalf("join exited %d", status)
	}
	for _, want := range []string{"project: demo", "member: dev", "hub role: member", "policy: verified", "policy version: 1", "no policy role yet", "openplexer install claude-code", "openplexer install codex", "openplexer service install", "requires an agent session"} {
		if !strings.Contains(out+stderr, want) {
			t.Errorf("join omitted %q", want)
		}
	}
	r, err := projects.Read(user)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Lookup(root)
	if err != nil || p.Root != root || filepath.Dir(p.Home) != filepath.Join(user, ".openplexer", "homes") {
		t.Fatal("join did not register the nearest checkout's home")
	}
	config, err := os.ReadFile(filepath.Join(p.Home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(config, &cfg) != nil || cfg.Token == "" || strings.Contains(out+stderr, cfg.Token) {
		t.Fatal("join mishandled its saved bearer")
	}
	for _, name := range []string{".claude", ".codex"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatal("join installed a hook without consent")
		}
	}
	if _, err := os.Stat(filepath.Join(user, "Library")); !os.IsNotExist(err) {
		t.Fatal("join touched the login service without consent")
	}
	if _, err := os.Stat(filepath.Join(user, "config")); !os.IsNotExist(err) {
		t.Fatal("join touched the login service without consent")
	}
}

func TestJoinCommandRejectsLinkArgumentsAndExtraInput(t *testing.T) {
	c, _, _, code := joinCLI(t)
	for _, args := range [][]string{{"join", strings.TrimSpace(c.stdin)}, {"join", "--" + strings.TrimSpace(c.stdin)}, {"join", "--home", strings.TrimSpace(c.stdin)}, {"join"}} {
		status, out, stderr := c.run(args...)
		if status == exitOK || strings.Contains(out+stderr, code) {
			t.Fatal("join accepted or exposed an unsafe input")
		}
	}
	c.stdin += c.stdin
	if status, out, stderr := c.run("join", "--link-stdin"); status == exitOK || strings.Contains(out+stderr, code) {
		t.Fatal("join accepted or exposed multiple links")
	}
}

func TestJoinCommandOptionalInstalls(t *testing.T) {
	c, user, root, code := joinCLI(t)
	home := filepath.Join(user, "home")
	t.Setenv("JOIN_TEST_HOME", home)
	shims := t.TempDir()
	t.Setenv("PATH", shims+string(os.PathListSeparator)+os.Getenv("PATH"))
	manager, show, start, missing, running := "systemctl", "show", "enable", "LoadState=not-found", "LoadState=loaded\nActiveState=active\nUnitFileState=enabled\nMainPID=35353"
	if runtime.GOOS == "darwin" {
		manager, show, start, missing, running = "launchctl", "print", "bootstrap", "Could not find service", "state = running\npid = 35353"
	}
	script := "#!/bin/sh\nif [ \"$1\" = --user ]; then shift; fi\n" +
		"case \"$1\" in\n" + show + ")\nif [ -e \"$JOIN_TEST_HOME/daemon.pid\" ]; then\nprintf '%s\\n' '" + running + "'\nelse\nprintf '%s\\n' '" + missing + "'\nexit 1\nfi;;\n" +
		start + ")\nstamp=$(/bin/date -u +%Y-%m-%dT%H:%M:%S)\nprintf '{\"pid\":35353,\"started_at\":\"%s.999999999Z\",\"heartbeat_at\":\"%sZ\"}\\n' \"$stamp\" \"$stamp\" > \"$JOIN_TEST_HOME/daemon.pid\";;\nesac\n"
	if err := os.WriteFile(filepath.Join(shims, manager), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	status, out, stderr := c.run("join", "--link-stdin", "--home", home, "--install-hook", "--install-service")
	if status != exitOK || strings.Contains(out+stderr, code) || !strings.Contains(out, "installed service") {
		t.Fatalf("join did not install with consent (exit %d)", status)
	}
	for _, path := range []string{".claude/settings.json", ".codex/hooks.json"} {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || !strings.Contains(string(data), "--project-home") {
			t.Fatalf("portable hook is missing at %s", path)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "daemon.pid")); err != nil {
		t.Fatal("fake manager did not start service")
	}
}

func TestJoinRepositoryMatch(t *testing.T) {
	for _, tc := range []struct {
		repo, remote string
		match        bool
	}{
		{"team/demo", "git@host:team/demo.git", true},
		{"team/demo", "https://host/team/demo", true},
		{"host/team/demo", "ssh://git@host/team/demo.git", true},
		{"ssh://host/team/demo", "git@host:team/demo.git", true},
		{"https://host/team/demo", "git@host:team/demo.git", false},
		{"ssh://host:2222/team/demo", "git@host:team/demo.git", false},
		{"https://host/team/demo", "http://host/team/demo", false},
		{"https://host:8443/team/demo", "https://host:9443/team/demo", false},
		{"https://host:8443/team/demo", "https://host/team/demo", false},
		{"https://host/team/demo", "https://HOST:443/team/demo.git", true},
		{"https://host:8443/team/demo", "https://host:8443/team/demo.git", true},
		{"https://host/team/demo", "https://host/team/Demo", false},
		{"https://other/team/demo", "git@host:team/demo.git", false},
		{"team/demo", "git@host:team/another.git", false},
		{"team/demo", "", false},
	} {
		if got := sameRepository(tc.repo, tc.remote); got != tc.match {
			t.Errorf("repository match for %q and %q = %t", tc.repo, tc.remote, got)
		}
	}
}

func TestJoinSkipsPortableHooksForDifferentOriginPort(t *testing.T) {
	c, _, root, code := joinCLIWithRepo(t, "https://git.example.test:8443/team/demo", "https://git.example.test:9443/team/demo")
	status, out, stderr := c.run("join", "--link-stdin", "--install-hook")
	if status != exitOK || !strings.Contains(out, "policy: verified") || strings.Contains(out+stderr, code) {
		t.Fatalf("join did not finish safely (exit %d)", status)
	}
	for _, path := range []string{".claude/settings.json", ".codex/hooks.json"} {
		if _, err := os.Lstat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Errorf("join wrote %s for a checkout on a different origin port", path)
		}
	}
	if !strings.Contains(out, "Git origin does not match the project repository") {
		t.Error("join omitted the reason it skipped portable hooks")
	}
}

func TestJoinOptionalInstallFailureLeavesUsableHome(t *testing.T) {
	c, _, _, code := joinCLI(t)
	shims := t.TempDir()
	t.Setenv("PATH", shims+string(os.PathListSeparator)+os.Getenv("PATH"))
	manager := "systemctl"
	if runtime.GOOS == "darwin" {
		manager = "launchctl"
	}
	if err := os.WriteFile(filepath.Join(shims, manager), []byte("#!/bin/sh\nexit 2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	status, out, stderr := c.run("join", "--link-stdin", "--install-service")
	if status != exitOK || !strings.Contains(out, "policy: verified") || !strings.Contains(out, "To start the login service") ||
		stderr == "" || strings.Contains(out+stderr, code) {
		t.Fatal("an optional install failure did not leave a usable joined home")
	}
	t.Setenv("OPENPLEXER_HOME", "")
	c.stdin = ""
	status, out, _ = c.run("policy", "status")
	if status != exitOK || !strings.Contains(out, "policy: verified") {
		t.Fatal("the registered policy cannot be used after optional setup failed")
	}
}
