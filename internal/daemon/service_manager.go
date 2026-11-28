package daemon

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	serviceEnv           = "OPENPLEXER_SERVICE"
	systemdServiceMarker = "Environment=" + serviceEnv + "=1"
	launchdServiceMarker = "    <key>" + serviceEnv + "</key>\n    <string>1</string>"
)

type serviceRunner interface {
	run(context.Context, string, ...string) ([]byte, error)
}

type execServiceRunner struct{}

func (execServiceRunner) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	return cmd.CombinedOutput()
}

func (s *Service) definition(binary string) ([]byte, error) {
	if s.platform == "linux" {
		// ExecStart checks the executable after unquoting and specifier expansion.
		if !systemdTextValid(binary) || strings.ContainsAny(binary, "'\"\\") ||
			strings.ContainsFunc(binary, func(r rune) bool { return r < ' ' || r == 0x7f }) {
			return nil, errors.New("systemd cannot use this executable path in ExecStart. Install the binary in a path without quotes, backslashes, or control characters, using valid UTF-8")
		}
		// Environment values allow quotes, backslashes, and controls other than NUL.
		if !systemdTextValid(s.Home) {
			return nil, errors.New("systemd cannot set OPENPLEXER_HOME from this path. Use a home path with valid UTF-8, no NUL, and no Unicode noncharacters")
		}
		return fmt.Appendf(nil, `[Unit]
Description=OpenPlexer daemon

[Service]
Type=exec
ExecStart=%s daemon --quiet
Environment=%s
%s
Restart=always
RestartSec=5s
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target
`, systemdQuote(":"+binary), systemdQuote("OPENPLEXER_HOME="+s.Home), systemdServiceMarker), nil
	}

	paths := []string{binary, s.Home, s.stdout, s.stderr}
	for i, path := range paths {
		if !utf8.ValidString(path) || strings.ContainsFunc(path, func(r rune) bool {
			return r < 32 && r != '\t' && r != '\n' && r != '\r' || r == 0xfffe || r == 0xffff
		}) {
			return nil, errors.New("the executable and home paths must be valid XML text for LaunchAgents")
		}
		var out strings.Builder
		if err := xml.EscapeText(&out, []byte(path)); err != nil {
			return nil, err
		}
		paths[i] = out.String()
	}
	return fmt.Appendf(nil, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>daemon</string>
    <string>--quiet</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>OPENPLEXER_HOME</key>
    <string>%s</string>
%s
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>Umask</key>
  <integer>63</integer>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, s.Name, paths[0], paths[1], launchdServiceMarker, paths[2], paths[3]), nil
}

func (s *Service) definitionMarker() string {
	if s.platform == "linux" {
		return systemdServiceMarker
	}
	return launchdServiceMarker
}

func systemdQuote(word string) string {
	return strconv.Quote(strings.ReplaceAll(word, "%", "%%"))
}

func systemdTextValid(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsFunc(value, func(r rune) bool {
		return r == 0 || r >= 0xfdd0 && r <= 0xfdef || r&0xffff >= 0xfffe
	})
}

func (s *Service) prepareLogs() error {
	dir := filepath.Dir(s.stdout)
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s must be a directory", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	for _, path := range []string{s.stdout, s.stderr} {
		info, err := os.Lstat(path)
		if err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("%s must be a regular log file", path)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		err = f.Chmod(0o600)
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
	}
	return nil
}

func (s *Service) start(current ServiceStatus) error {
	ctx := context.Background()
	if s.platform == "linux" {
		_, err := s.systemctl(ctx, "enable", "--now", s.path)
		return err
	}
	target := s.domain + "/" + s.Name
	if _, err := s.launchctl(ctx, "enable", target); err != nil {
		return err
	}
	if current.PID > 0 {
		return nil
	}
	if current.loaded {
		_, err := s.launchctl(ctx, "kickstart", target)
		return err
	}
	_, err := s.launchctl(ctx, "bootstrap", s.domain, s.path)
	return err
}

func (s *Service) stop(current ServiceStatus) error {
	ctx := context.Background()
	if s.platform == "linux" {
		if current.loaded {
			if _, err := s.systemctl(ctx, "stop", s.Name); err != nil {
				return err
			}
		}
		_, err := s.systemctl(ctx, "disable", s.Name)
		return err
	}
	target := s.domain + "/" + s.Name
	if _, err := s.launchctl(ctx, "disable", target); err != nil {
		return err
	}
	if current.loaded {
		out, err := s.launchctl(ctx, "bootout", target)
		if err != nil && !missingLaunchService(out) {
			return err
		}
	}
	return nil
}

func (s *Service) managerStatus(ctx context.Context) (ServiceStatus, error) {
	if s.platform == "darwin" {
		out, err := s.launchctl(ctx, "print", s.domain+"/"+s.Name)
		if err != nil && missingLaunchService(out) {
			return ServiceStatus{State: "missing"}, nil
		}
		if err != nil {
			return ServiceStatus{}, err
		}
		fields := managerFields(out, " = ")
		if fields["state"] == "" {
			return ServiceStatus{}, errors.New("launchctl print did not report the service state")
		}
		status := ServiceStatus{State: "stopped", loaded: true}
		status.PID, _ = strconv.Atoi(fields["pid"])
		if code, err := strconv.Atoi(fields["last exit code"]); err == nil && code != 0 {
			status.Failure = fmt.Sprintf("last exit code %d", code)
		}
		if _, signal, ok := strings.Cut(fields["last terminating signal"], ": "); ok {
			if n, err := strconv.Atoi(signal); err == nil && n > 0 {
				status.Failure = fmt.Sprintf("last terminating signal %d", n)
			}
		}
		switch {
		case fields["state"] == "running" && status.PID > 0:
			status.State = "running"
		case status.Failure != "":
			status.State = "failed"
		}
		return status, nil
	}
	out, err := s.systemctl(ctx, "show", "--property=LoadState,ActiveState,MainPID,UnitFileState,Result,ExecMainStatus", s.Name)
	fields := managerFields(out, "=")
	pid, _ := strconv.Atoi(fields["MainPID"])
	if fields["LoadState"] == "not-found" && pid == 0 {
		return ServiceStatus{State: "missing"}, nil
	}
	if err != nil {
		return ServiceStatus{}, err
	}
	if fields["LoadState"] == "" || fields["ActiveState"] == "" {
		return ServiceStatus{}, errors.New("systemctl --user show did not report the unit state")
	}
	status := ServiceStatus{State: "stopped", PID: pid, loaded: true, enabled: fields["UnitFileState"] == "enabled"}
	switch result := fields["Result"]; result {
	case "exit-code", "signal", "core-dump", "timeout", "watchdog", "start-limit-hit", "resources", "protocol", "oom-kill":
		status.Failure = "last result " + result
		if n, err := strconv.Atoi(fields["ExecMainStatus"]); err == nil && n > 0 {
			status.Failure += fmt.Sprintf(" (status %d)", n)
		}
	}
	switch fields["LoadState"] {
	case "error", "bad-setting", "masked":
		status.State = "failed"
		status.Failure = "unit load state " + fields["LoadState"]
	default:
		switch {
		case fields["ActiveState"] == "active" && status.PID > 0:
			status.State = "running"
		case fields["ActiveState"] == "failed" || status.Failure != "":
			status.State = "failed"
		}
	}
	return status, nil
}

func managerFields(out []byte, separator string) map[string]string {
	fields := map[string]string{}
	for line := range strings.Lines(string(out)) {
		key, value, ok := strings.Cut(strings.TrimSpace(line), separator)
		if ok {
			if _, seen := fields[key]; !seen {
				fields[key] = value
			}
		}
	}
	return fields
}

func missingLaunchService(out []byte) bool {
	return strings.Contains(string(out), "Could not find service")
}

func (s *Service) systemctl(ctx context.Context, args ...string) ([]byte, error) {
	return s.runManager(ctx, "systemctl", args[0], append([]string{"--user"}, args...)...)
}

func (s *Service) launchctl(ctx context.Context, args ...string) ([]byte, error) {
	return s.runManager(ctx, "launchctl", args[0], args...)
}

func (s *Service) runManager(ctx context.Context, name, action string, args ...string) ([]byte, error) {
	out, err := s.runner.run(ctx, name, args...)
	if err == nil {
		return out, nil
	}
	// Manager output can include the service environment. Report only safe fields.
	if errors.Is(err, exec.ErrNotFound) {
		return out, fmt.Errorf("%s is unavailable", name)
	}
	if strings.Contains(string(out), "Could not find domain") || strings.Contains(string(out), "Failed to connect to bus") {
		return out, fmt.Errorf("%s could not reach the user service manager", name)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out, fmt.Errorf("%s %s exited with status %d", name, action, exit.ExitCode())
	}
	return out, fmt.Errorf("could not run %s %s", name, action)
}
