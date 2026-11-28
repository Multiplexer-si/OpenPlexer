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
	t         *testing.T
	calls     []serviceCall
	onSuccess func([]string)
}

func (r *fakeServiceRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
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
	if want.err == nil && r.onSuccess != nil {
		r.onSuccess(got)
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

func holdServiceTestRoom(t *testing.T, home string, pid int, mode string) {
	t.Helper()
	file, err := lockRoom(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	started := time.Now().UTC()
	lease, err := (DaemonActivity{PID: pid, StartedAt: started, HeartbeatAt: started, Mode: mode}).publish(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lease.Close() })
}

func TestServiceCaseAlias(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	home, alias := filepath.Join(user, "PolicyHome"), filepath.Join(user, "policyhome")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(alias); errors.Is(err, os.ErrNotExist) {
		t.Skip("the test volume is case-sensitive")
	} else if err != nil {
		t.Fatal(err)
	}
	r := &fakeServiceRunner{t: t}
	original, err := newService(home, "darwin", r)
	if err != nil {
		t.Fatal(err)
	}
	aliased, err := newService(alias, "darwin", r)
	if err != nil {
		t.Fatal(err)
	}
	if aliased.Name != original.Name || aliased.Home != original.Home {
		t.Fatal("case alias selected a different home or service")
	}
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
    <key>OPENPLEXER_SERVICE</key>
    <string>1</string>
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
Environment=OPENPLEXER_SERVICE=1
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
			runner.calls = append(runner.calls, running, running, running)
			runner.calls = append(runner.calls, again...)
			runner.calls = append(runner.calls, running, running)
			runner.calls = append(runner.calls, uninstall...)
			runner.calls = append(runner.calls, missing, missing)
			runner.onSuccess = func(args []string) {
				if slices.Equal(args, install[len(install)-1].args) {
					holdServiceTestRoom(t, home, 42, daemonManaged)
				}
			}
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
	t.Setenv(serviceEnv, "")
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
		{missingUnit(second.Name).args, "LoadState=loaded\nActiveState=active\nMainPID=42\n", nil},
	}
	r2.onSuccess = func(args []string) {
		if args[2] == "enable" {
			holdServiceTestRoom(t, two, 42, daemonManaged)
		}
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

type serviceRunnerFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

func (run serviceRunnerFunc) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return run(ctx, name, args...)
}

func reinstallService(t *testing.T, platform string, snapshot func() int) *Service {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	serviceTestHome(t, home)
	runner := serviceRunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "launchctl" && args[0] == "print":
			return fmt.Appendf(nil, "state = running\npid = %d\n", snapshot()), nil
		case name == "systemctl" && args[1] == "show":
			return fmt.Appendf(nil, "LoadState=loaded\nActiveState=active\nMainPID=%d\nUnitFileState=enabled\n", snapshot()), nil
		case name == "launchctl" && args[0] == "enable":
			return nil, nil
		default:
			t.Fatalf("unexpected manager command %s %v", name, args)
			return nil, nil
		}
	})
	s, err := newService(home, platform, runner)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := s.definition("/opt/openplexer")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Dir(s.path), filepath.Base(s.path), definition); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestServiceReinstallWaitsThroughPIDReuse(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			const pid = 2000000001
			var home string
			var snapshots int
			var ready bool
			s := reinstallService(t, platform, func() int {
				snapshots++
				if snapshots == 3 {
					holdServiceTestRoom(t, home, pid, daemonManaged)
					ready = true
				}
				return pid
			})
			home = s.Home
			heartbeat := time.Now().UTC()
			lease, err := (DaemonActivity{
				PID: pid, StartedAt: heartbeat.Add(-time.Minute), HeartbeatAt: heartbeat, Mode: daemonManaged,
			}).publish(home)
			if err != nil {
				t.Fatal(err)
			}
			// A killed daemon leaves its fresh heartbeat on disk, but loses the lease.
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
			if err := s.Install("/opt/openplexer"); err != nil {
				t.Fatal(err)
			}
			if !ready {
				t.Fatal("reinstall accepted the dead daemon's record before the replacement held the Room lock")
			}
		})
	}
}

func TestServiceReinstallAcceptsReplacementDuringPreflight(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			const oldPID, newPID = 2000000001, 2000000002
			var home string
			var sampled bool
			s := reinstallService(t, platform, func() int {
				if !sampled {
					sampled = true
					// Capture the old PID, then let the replacement acquire the lock
					// before the first manager call returns to the installer.
					holdServiceTestRoom(t, home, newPID, daemonManaged)
					return oldPID
				}
				return newPID
			})
			home = s.Home
			if err := s.Install("/opt/openplexer"); err != nil {
				t.Fatalf("reinstall rejected a healthy replacement: %v", err)
			}
		})
	}
}

func TestServiceReinstallRejectsForegroundWithReusedPID(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			s := reinstallService(t, platform, os.Getpid)
			holdServiceTestRoom(t, s.Home, os.Getpid(), daemonForeground)
			err := s.Install("/opt/openplexer")
			if err == nil || !strings.Contains(err.Error(), "stop the foreground openplexer daemon") {
				t.Fatalf("reinstall missed the foreground Room lock: %v", err)
			}
		})
	}
}

func TestServiceWaitsForDaemonAfterManagerReportsPID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(serviceEnv, "1")
	home := t.TempDir()
	serviceTestHome(t, home)
	stale := time.Date(2001, time.January, 1, 0, 0, 0, 0, time.UTC)
	if err := writeJSON(home, daemonPIDFile, DaemonActivity{PID: os.Getpid(), StartedAt: stale, HeartbeatAt: stale}); err != nil {
		t.Fatal(err)
	}
	streaming := make(chan struct{})
	useHubTransport(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/stream") {
			close(streaming)
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 404, Body: http.NoBody}, nil
	}))
	daemonCtx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	var launched bool
	defer func() {
		cancel()
		if launched {
			if err := <-done; err != nil {
				t.Error(err)
			}
		}
	}()
	var started bool
	var polls int
	runner := serviceRunnerFunc(func(ctx context.Context, _ string, args ...string) ([]byte, error) {
		if args[1] == "enable" {
			started = true
			return nil, nil
		}
		if !started {
			return []byte("LoadState=not-found\n"), nil
		}
		polls++
		switch polls {
		case 2, 3:
			activity := DaemonActivity{PID: os.Getpid(), StartedAt: stale, HeartbeatAt: time.Now().UTC()}
			if polls == 3 {
				activity.StartedAt, activity.HeartbeatAt = activity.HeartbeatAt, activity.StartedAt
			}
			if err := writeJSON(home, daemonPIDFile, activity); err != nil {
				t.Fatal(err)
			}
		case 4:
			if _, err := os.Stat(filepath.Join(home, "room.lock")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("installer touched the Room lock before the daemon started: %v", err)
			}
			launched = true
			go func() { done <- Run(daemonCtx, home, time.Now, nil, func(error) {}) }()
			select {
			case <-streaming:
			case err := <-done:
				launched = false
				t.Fatalf("daemon exited during startup: %v", err)
			case <-ctx.Done():
				t.Fatal("daemon did not finish starting")
			}
		}
		return fmt.Appendf(nil, "LoadState=loaded\nActiveState=active\nMainPID=%d\n", os.Getpid()), nil
	})
	s, err := newService(home, "linux", runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Install("/opt/openplexer"); err != nil {
		t.Fatal(err)
	}
	if !launched {
		t.Fatalf("install accepted a stale record for the live PID on poll %d", polls)
	}
	path := filepath.Join(home, daemonPIDFile)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("expected an owner-only daemon record: %v", err)
	}
	activity, err := ReadDaemonActivity(home)
	if err != nil || activity.PID != os.Getpid() || activity.StartedAt.IsZero() || activity.HeartbeatAt.IsZero() {
		t.Fatalf("daemon did not publish its activity: %+v, %v", activity, err)
	}
	cancel()
	launched = false
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clean daemon exit left its record: %v", err)
	}
}

func TestServiceReinstallChecksRunningJobOwnsRoom(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			home := t.TempDir()
			serviceTestHome(t, home)
			const binary = "/opt/openplexer"
			var checks int
			runner := serviceRunnerFunc(func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if args[0] == "enable" {
					return nil, nil
				}
				checks++
				if checks == 2 {
					holdServiceTestRoom(t, home, 43, daemonForeground)
				}
				if platform == "darwin" {
					return []byte("state = running\npid = 42\n"), nil
				}
				return []byte("LoadState=loaded\nActiveState=active\nMainPID=42\nUnitFileState=enabled\n"), nil
			})
			s, err := newService(home, platform, runner)
			if err != nil {
				t.Fatal(err)
			}
			definition, err := s.definition(binary)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeFile(filepath.Dir(s.path), filepath.Base(s.path), definition); err != nil {
				t.Fatal(err)
			}
			if err := s.Install(binary); err == nil || !strings.Contains(err.Error(), "stop the foreground openplexer daemon") {
				t.Fatalf("reinstall did not report the foreground daemon: %v", err)
			}
		})
	}
}

func TestServiceForegroundWinsDuringStart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	serviceTestHome(t, home)
	starting, resume := make(chan struct{}), make(chan struct{})
	var started, reportedPID bool
	runner := serviceRunnerFunc(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[1] {
		case "enable":
			close(starting)
			select {
			case <-resume:
			case <-t.Context().Done():
			}
			started = true
		case "show":
			switch {
			case !started:
				return []byte("LoadState=not-found\n"), nil
			case !reportedPID:
				reportedPID = true
				return []byte("LoadState=loaded\nActiveState=active\nMainPID=42\n"), nil
			default:
				return []byte("LoadState=loaded\nActiveState=activating\nResult=exit-code\nExecMainStatus=1\n"), nil
			}
		}
		return nil, nil
	})
	s, err := newService(home, "linux", runner)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Install("/opt/openplexer") }()
	select {
	case <-starting:
	case err := <-done:
		t.Fatalf("install returned before the manager started: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("install did not reach the manager")
	}
	holdServiceTestRoom(t, home, 43, daemonForeground)
	close(resume)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "stop the foreground openplexer daemon") ||
			!strings.Contains(err.Error(), "last result exit-code (status 1)") ||
			!strings.Contains(err.Error(), s.LogLocation()) {
			t.Fatalf("install did not report the startup failure with recovery steps: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("install did not finish within the startup timeout")
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
		{missing.args, "LoadState=loaded\nActiveState=active\nMainPID=42\n", nil},
		missing,
		{[]string{"systemctl", "--user", "disable", s.Name}, "", errors.New("manager unavailable")},
	}
	r.onSuccess = func(args []string) {
		if slices.Equal(args, enable) {
			holdServiceTestRoom(t, home, 42, daemonManaged)
		}
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
