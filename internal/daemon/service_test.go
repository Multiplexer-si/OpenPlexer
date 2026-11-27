package daemon

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

type serviceCall struct {
	args []string
	out  string
	err  error
}

type fakeServiceRunner struct {
	t     *testing.T
	calls []serviceCall
}

func (r *fakeServiceRunner) run(name string, args ...string) ([]byte, error) {
	r.t.Helper()
	got := append([]string{name}, args...)
	if len(r.calls) == 0 {
		r.t.Fatalf("unexpected manager command %v", got)
	}
	want := r.calls[0]
	r.calls = r.calls[1:]
	if !slices.Equal(got, want.args) {
		r.t.Fatalf("manager command = %v, want %v", got, want.args)
	}
	return []byte(want.out), want.err
}

const showProperties = "--property=LoadState,ActiveState,MainPID,UnitFileState,Result,ExecMainStatus"

func missingUnit(name string) serviceCall {
	return serviceCall{
		args: []string{"systemctl", "--user", "show", showProperties, name},
		out:  "LoadState=not-found\nActiveState=inactive\n",
		err:  errors.New("unit missing"),
	}
}

func serviceTestHome(t *testing.T, home string) {
	t.Helper()
	configure(t, home, Settings{
		HubURL: testHubOrigin, ProjectID: "prj-shop", MemberID: "mem-ana",
		Token: strings.Repeat("A", 43), KeySet: newSigner(t).keySet(t),
	})
}

func TestServiceLifecycle(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", dir)
			xdg := filepath.Join(dir, "xdg")
			t.Setenv("XDG_CONFIG_HOME", xdg)
			home := filepath.Join(dir, "project & home %")
			serviceTestHome(t, home)
			alias := filepath.Join(dir, "alias")
			if err := os.Symlink(home, alias); err != nil {
				t.Fatal(err)
			}
			runner := &fakeServiceRunner{t: t}
			s, err := newService(alias, platform, runner)
			if err != nil {
				t.Fatal(err)
			}
			second, err := newService(home, platform, runner)
			if err != nil {
				t.Fatal(err)
			}
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(home)))
			binary := "/opt/OpenPlexer % $OPENPLEXER_HOME/openplexer"
			var name, path, definition, failure, logs string
			var missing, running serviceCall
			var install, again, uninstall []serviceCall
			switch platform {
			case "darwin":
				name = "com.openplexer.daemon." + hash
				path = filepath.Join(dir, "Library", "LaunchAgents", name+".plist")
				domain := fmt.Sprintf("gui/%d", os.Getuid())
				target := domain + "/" + name
				missing = serviceCall{[]string{"launchctl", "print", target}, "Could not find service", errors.New("missing")}
				running = serviceCall{[]string{"launchctl", "print", target}, "state = running\npid = 42\nlast exit code = 3", nil}
				enable := serviceCall{args: []string{"launchctl", "enable", target}}
				install = []serviceCall{enable, {args: []string{"launchctl", "bootstrap", domain, path}}}
				again = []serviceCall{enable}
				uninstall = []serviceCall{
					{args: []string{"launchctl", "disable", target}},
					{args: []string{"launchctl", "bootout", target}},
				}
				failure = "last exit code 3"
				logs = filepath.Join(home, "logs", "daemon.stdout.log") + "\n" + filepath.Join(home, "logs", "daemon.stderr.log")
				definition = strings.NewReplacer("@NAME@", name, "@HOME@", strings.ReplaceAll(home, "&", "&amp;")).Replace(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>@NAME@</string>
  <key>ProgramArguments</key>
  <array>
    <string>/opt/OpenPlexer % $OPENPLEXER_HOME/openplexer</string>
    <string>daemon</string>
    <string>--quiet</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>OPENPLEXER_HOME</key>
    <string>@HOME@</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>Umask</key>
  <integer>63</integer>
  <key>StandardOutPath</key>
  <string>@HOME@/logs/daemon.stdout.log</string>
  <key>StandardErrorPath</key>
  <string>@HOME@/logs/daemon.stderr.log</string>
</dict>
</plist>
`)
			case "linux":
				name = "openplexer-daemon-" + hash + ".service"
				path = filepath.Join(xdg, "systemd", "user", name)
				missing = missingUnit(name)
				running = serviceCall{missing.args, "LoadState=loaded\nActiveState=active\nMainPID=42\nUnitFileState=enabled\nResult=signal\nExecMainStatus=9\n", nil}
				install = []serviceCall{{args: []string{"systemctl", "--user", "enable", "--now", path}}}
				uninstall = []serviceCall{
					{args: []string{"systemctl", "--user", "stop", name}},
					{args: []string{"systemctl", "--user", "disable", name}},
					{args: []string{"systemctl", "--user", "daemon-reload"}},
				}
				failure = "last result signal (status 9)"
				logs = "journalctl --user -u " + name
				definition = strings.ReplaceAll(`[Unit]
Description=OpenPlexer daemon

[Service]
Type=exec
ExecStart=":/opt/OpenPlexer %% $OPENPLEXER_HOME/openplexer" daemon --quiet
Environment="OPENPLEXER_HOME=@HOME@"
Restart=always
RestartSec=5s
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target
`, "@HOME@", strings.ReplaceAll(home, "%", "%%"))
			}
			if s.Name != name || second.Name != name || s.Home != home || s.path != path || s.LogLocation() != logs {
				t.Fatal("incorrect service name, canonical home, definition path, or log location")
			}

			runner.calls = append(runner.calls, missing, missing)
			runner.calls = append(runner.calls, install...)
			runner.calls = append(runner.calls, running, running)
			runner.calls = append(runner.calls, again...)
			runner.calls = append(runner.calls, running)
			runner.calls = append(runner.calls, uninstall...)
			runner.calls = append(runner.calls, missing, missing)
			checkStatus := func(want string, pid int, failure string) {
				t.Helper()
				status, err := s.Status()
				if err != nil || status.State != want || status.PID != pid || status.Failure != failure {
					t.Fatalf("status = %+v, %v, want %s, pid %d, %q", status, err, want, pid, failure)
				}
			}
			checkStatus("missing", 0, "")
			if err := s.Install(binary); err != nil {
				t.Fatal(err)
			}
			if string(readFile(t, path)) != definition {
				t.Fatal("generated service definition differs from expected bytes")
			}
			checkStatus("running", 42, failure)
			if err := second.Install(binary); err != nil {
				t.Fatal(err)
			}
			if platform == "darwin" {
				for path, want := range map[string]os.FileMode{
					filepath.Dir(s.stdout): 0o700, s.stdout: 0o600, s.stderr: 0o600,
				} {
					info, err := os.Stat(path)
					if err != nil || info.Mode().Perm() != want {
						t.Fatalf("expected %s with mode %o", path, want)
					}
				}
				if err := os.WriteFile(s.stderr, []byte("log entry"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeJSON(home, roomCursorFile, roomCursor{LastSeq: 7}); err != nil {
				t.Fatal(err)
			}
			if err := writeJSON(home, policyFile, storedPolicy{ConfirmedAt: loadedAt}); err != nil {
				t.Fatal(err)
			}
			policy := string(readFile(t, filepath.Join(home, policyFile)))
			cursor := string(readFile(t, filepath.Join(home, roomCursorFile)))
			if err := s.Uninstall(); err != nil {
				t.Fatal(err)
			}
			if err := s.Uninstall(); err != nil {
				t.Fatal(err)
			}
			checkStatus("missing", 0, "")
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("uninstall left the definition")
			}
			if string(readFile(t, filepath.Join(home, policyFile))) != policy ||
				string(readFile(t, filepath.Join(home, roomCursorFile))) != cursor ||
				platform == "darwin" && string(readFile(t, s.stderr)) != "log entry" {
				t.Fatal("uninstall changed policy, cursor, or logs")
			}
			if len(runner.calls) != 0 {
				t.Fatal("manager calls were skipped")
			}
		})
	}
}

func TestServiceForegroundLockAndIsolation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "relative-config")
	one, two := t.TempDir(), t.TempDir()
	serviceTestHome(t, one)
	serviceTestHome(t, two)
	streaming := make(chan struct{})
	useHubTransport(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/stream") {
			close(streaming)
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 404, Body: http.NoBody}, nil
	}))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	done := make(chan error, 1)
	go func() { done <- Run(ctx, one, time.Now, nil, func(error) {}) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-streaming:
	case <-ctx.Done():
		t.Fatal("foreground daemon did not start")
	}
	newLinux := func(home string) (*Service, *fakeServiceRunner) {
		t.Helper()
		r := &fakeServiceRunner{t: t}
		s, err := newService(home, "linux", r)
		if err != nil {
			t.Fatal(err)
		}
		return s, r
	}
	first, r1 := newLinux(one)
	second, r2 := newLinux(two)
	r1.calls = []serviceCall{missingUnit(first.Name)}
	r2.calls = []serviceCall{
		missingUnit(second.Name),
		{args: []string{"systemctl", "--user", "enable", "--now", second.path}},
	}
	if first.Name == second.Name || second.path != filepath.Join(os.Getenv("HOME"), ".config", "systemd", "user", second.Name) {
		t.Fatal("homes were not isolated in the default systemd config directory")
	}
	const binary = "/opt/openplexer"
	if err := first.Install(binary); err == nil || !strings.Contains(err.Error(), "stop the foreground openplexer daemon") {
		t.Fatal("install did not explain the foreground lock conflict")
	}
	if _, err := os.Stat(first.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("install wrote a definition despite the foreground lock")
	}
	if err := Run(ctx, one, time.Now, nil, nil); !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatal("a second daemon acquired the foreground daemon's lock")
	}
	if err := second.Install(binary); err != nil {
		t.Fatal(err)
	}
	if len(r1.calls)+len(r2.calls) != 0 {
		t.Fatal("manager calls were skipped")
	}
}

func TestServiceFailedStartAndRetry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	r := &fakeServiceRunner{t: t}
	s, err := newService(home, "linux", r)
	if err != nil {
		t.Fatal(err)
	}
	const binary = "/opt/openplexer"
	if err := s.Install(binary); err == nil || !strings.Contains(err.Error(), "configure a hub first") {
		t.Fatal("install accepted a home without hub configuration")
	}
	serviceTestHome(t, home)
	const sensitive = "test credential marker"
	missing := missingUnit(s.Name)
	failed := serviceCall{missing.args, "LoadState=loaded\nActiveState=failed\nResult=exit-code\nExecMainStatus=1\nEnvironment=" + sensitive, nil}
	enable := []string{"systemctl", "--user", "enable", "--now", s.path}
	r.calls = []serviceCall{
		missing,
		{enable, sensitive, errors.New(sensitive)},
		missing,
		failed,
		failed,
		{args: enable},
		missing,
		{[]string{"systemctl", "--user", "disable", s.Name}, "", errors.New("manager unavailable")},
	}
	if err := s.Install(binary); err == nil || !strings.Contains(err.Error(), "could not start") ||
		strings.Contains(err.Error(), sensitive) {
		t.Fatal("install did not report the start error safely")
	}
	if status, err := s.Status(); err != nil || status.State != "stopped" || status.PID != 0 {
		t.Fatal("status did not report a stopped definition after a start error")
	}
	if status, err := s.Status(); err != nil || status.State != "failed" || status.Failure != "last result exit-code (status 1)" {
		t.Fatal("status did not report the manager failure safely")
	}
	if err := s.Install(binary); err != nil {
		t.Fatal(err)
	}
	if err := s.Uninstall(); err == nil {
		t.Fatal("uninstall hid a manager error")
	}
	if _, err := os.Stat(s.path); err != nil {
		t.Fatal("uninstall removed the definition before disabling the service")
	}
	if len(r.calls) != 0 {
		t.Fatal("manager calls were skipped")
	}
}
