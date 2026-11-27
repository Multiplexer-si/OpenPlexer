//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/multiplexer-si/openplexer/internal/daemon"
	"github.com/multiplexer-si/openplexer/internal/hub"
)

func TestRealDaemonService(t *testing.T) {
	if os.Getenv("OPENPLEXER_REAL_SERVICE_TEST") != "1" {
		t.Skip("set OPENPLEXER_REAL_SERVICE_TEST=1 in a macOS or Linux login session")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "openplexer % $binary")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build openplexer: %v\n%s", err, out)
	}

	home := filepath.Join(dir, "openplexer home")
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("OPENPLEXER_HOME", filepath.Join(dir, "unused-home"))
	if runtime.GOOS == "linux" {
		useRuntimeSystemdUnits(t, dir)
	}

	store := hub.Open(filepath.Join(dir, "hub"))
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	const project, member = "prj-shop", "mem-ana"
	token, err := store.AddMember(member, hub.Member)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddProject(project, "api"); err != nil {
		t.Fatal(err)
	}
	handler := store.Handler()
	var rejectRoom atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rejectRoom.Load() && strings.HasSuffix(r.URL.Path, "/stream") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})
	keys := httptest.NewRecorder()
	handler.ServeHTTP(keys, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
	if keys.Code != http.StatusOK {
		t.Fatalf("hub keys returned HTTP %d", keys.Code)
	}
	if err := daemon.Configure(home, daemon.Settings{
		HubURL: server.URL, ProjectID: project, MemberID: member, Token: token, KeySet: keys.Body.Bytes(),
	}); err != nil {
		t.Fatal(err)
	}
	publish := func(version int) {
		t.Helper()
		var bundle map[string]any
		readJSON(t, filepath.Join(policySpec, "examples", "bundle", "minimal.json"), &bundle)
		bundle["repos"], bundle["version"] = []string{"api"}, version
		text, err := json.Marshal(bundle)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.PublishPolicy(project, text); err != nil {
			t.Fatal(err)
		}
	}
	publish(1)
	s, err := daemon.NewService(home)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := s.Status(); err != nil || state.State != "missing" {
		t.Fatalf("expected an unused service name %s, got %s: %v", s.Name, state.State, err)
	}
	definition := filepath.Join(dir, "Library", "LaunchAgents", s.Name+".plist")
	if runtime.GOOS == "linux" {
		definition = filepath.Join(dir, "config", "systemd", "user", s.Name)
	}
	var created os.FileInfo
	ownsDefinition := func() bool {
		info, err := os.Lstat(definition)
		return err == nil && created != nil && os.SameFile(created, info)
	}
	c := cli{t: t, bin: binary}
	var uninstalled bool
	t.Cleanup(func() {
		if uninstalled || created == nil {
			return
		}
		// Cleanup runs after the test context has been canceled.
		for range 2 {
			if !ownsDefinition() {
				t.Error("the disposable service definition changed before cleanup")
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := exec.CommandContext(ctx, binary, "service", "uninstall", "--home", home).Run()
			cancel()
			if err == nil {
				return
			}
		}
		t.Errorf("could not clean up the disposable service %s", s.Name)
	})

	status, _, _ := c.run("service", "install", "--home", home)
	created, err = os.Lstat(definition)
	if status != exitOK || err != nil {
		t.Fatalf("disposable service install exited %d, definition lookup: %v", status, err)
	}
	var first int
	waitForService(t, 20*time.Second, "service to start and sync policy version 1", func() bool {
		state, err := s.Status()
		first = state.PID
		status, policy, _ := c.run("service", "status", "--home", home)
		return err == nil && state.State == "running" && first > 0 && status == exitOK &&
			strings.Contains(policy, "service: running") && strings.Contains(policy, "policy: verified") &&
			strings.Contains(policy, "version 1 of project "+project) && strings.Contains(policy, "home: "+home) &&
			strings.Contains(policy, s.LogLocation())
	})
	if !ownsDefinition() {
		t.Fatal("the disposable service definition changed before reinstall")
	}
	c.must(exitOK, "service", "install", "--home", home)
	if state, err := s.Status(); err != nil || state.State != "running" || state.PID != first {
		t.Fatal("the second install did not preserve the running daemon")
	}
	publish(2)
	waitForService(t, 17*time.Second, "policy version 2 to sync", func() bool {
		gate, err := daemon.Open(home, time.Now)
		return err == nil && gate.Status().State == daemon.Verified && gate.Status().Version == 2
	})

	const eventBody = "disposable-service-room-event"
	t.Setenv("OPENPLEXER_HOME", home)
	poster := c
	poster.stdin = `{"type":"status","agent":{"id":"session","vendor":"codex"},"body":{"summary":"` + eventBody + `"}}`
	poster.must(exitOK, "room", "post")
	waitForService(t, 10*time.Second, "Room cursor to advance", func() bool {
		data, err := os.ReadFile(filepath.Join(home, "room-cursor.json"))
		var cursor struct {
			LastSeq int64 `json:"last_seq"`
		}
		return err == nil && json.Unmarshal(data, &cursor) == nil && cursor.LastSeq > 0
	})

	if !ownsDefinition() {
		t.Fatal("the disposable service definition changed before the restart test")
	}
	if state, err := s.Status(); err != nil || state.State != "running" || state.PID != first {
		t.Fatal("the disposable service no longer owns the original daemon PID")
	}
	// Signal the manager's service target so PID reuse cannot signal another process.
	var kill *exec.Cmd
	if runtime.GOOS == "darwin" {
		kill = exec.CommandContext(t.Context(), "launchctl", "kill", "SIGKILL", fmt.Sprintf("gui/%d/%s", os.Getuid(), s.Name))
	} else {
		kill = exec.CommandContext(t.Context(), "systemctl", "--user", "kill", "--signal=SIGKILL", s.Name)
	}
	if err := kill.Run(); err != nil {
		t.Fatalf("could not signal the disposable service: %v", err)
	}
	var restarted int
	waitForService(t, 30*time.Second, "manager to restart the daemon", func() bool {
		state, err := s.Status()
		restarted = state.PID
		return err == nil && state.State == "running" && restarted > 0 && restarted != first
	})
	rejectRoom.Store(true)
	server.CloseClientConnections()
	waitForService(t, 15*time.Second, "quiet daemon to log a Room error", func() bool {
		var logs []byte
		if runtime.GOOS == "darwin" {
			for _, path := range strings.Split(s.LogLocation(), "\n") {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("expected a private log file at %s", path)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				logs = append(logs, data...)
			}
		} else {
			out, err := exec.CommandContext(t.Context(), "journalctl", "--user", "-u", s.Name, "--no-pager", "--output=cat").Output()
			if err != nil {
				t.Fatalf("could not read the service journal: %v", err)
			}
			logs = out
		}
		if bytes.Contains(logs, []byte(eventBody)) || bytes.Contains(logs, []byte(token)) {
			t.Fatal("quiet daemon logged a Room body or member credential")
		}
		return bytes.Contains(logs, []byte("Room stream: hub returned HTTP 503"))
	})
	if !ownsDefinition() {
		t.Fatal("the disposable service definition changed before uninstall")
	}
	c.must(exitOK, "service", "uninstall", "--home", home)
	uninstalled = true
	waitForService(t, 20*time.Second, "service and daemon process to stop", func() bool {
		status, err := s.Status()
		return err == nil && status.State == "missing" && status.PID == 0 &&
			errors.Is(syscall.Kill(restarted, syscall.Signal(0)), syscall.ESRCH)
	})
	if out := c.must(exitOK, "service", "status", "--home", home); !strings.Contains(out, "service: missing") ||
		!strings.Contains(out, "version 2 of project "+project) {
		t.Fatal("status did not preserve the local policy after uninstall")
	}
}

func useRuntimeSystemdUnits(t *testing.T, dir string) {
	t.Helper()
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		t.Fatal(err)
	}
	systemctl, err = filepath.Abs(systemctl)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENPLEXER_TEST_SYSTEMCTL", systemctl)
	// Keep enablement in the live user manager's runtime directory.
	shim := `#!/bin/sh
case "$2" in
  enable|disable) exec "$OPENPLEXER_TEST_SYSTEMCTL" --runtime "$@" ;;
  *) exec "$OPENPLEXER_TEST_SYSTEMCTL" "$@" ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func waitForService(t *testing.T, timeout time.Duration, goal string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, goal)
}
