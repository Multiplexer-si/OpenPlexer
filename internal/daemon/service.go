package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/multiplexer-si/openplexer/internal/projects"
)

// Service manages the login daemon for one canonical OpenPlexer home.
type Service struct {
	Home, Name   string
	path, domain string
	stdout       string
	stderr       string
	platform     string
	runner       serviceRunner
}

// ServiceStatus describes the manager's view of the daemon process.
type ServiceStatus struct {
	State, Failure  string
	PID             int
	Activity        DaemonActivity
	loaded, enabled bool
}

// NewService finds the service definition and logs for the selected home.
func NewService(home string) (*Service, error) {
	return newService(home, runtime.GOOS, execServiceRunner{})
}

func newService(home, platform string, runner serviceRunner) (*Service, error) {
	if platform != "darwin" && platform != "linux" {
		return nil, errors.New("daemon services require macOS or Linux")
	}
	if !filepath.IsAbs(home) {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		home = cwd + string(filepath.Separator) + home
	}
	home, err := canonicalServiceHome(home)
	if err != nil {
		return nil, err
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(home)))
	s := &Service{Home: home, platform: platform, runner: runner}
	if platform == "darwin" {
		s.Name = "com.openplexer.daemon." + hash
		s.path = filepath.Join(userHome, "Library", "LaunchAgents", s.Name+".plist")
		s.domain = fmt.Sprintf("gui/%d", os.Getuid())
		s.stdout = filepath.Join(home, "logs", "daemon.stdout.log")
		s.stderr = filepath.Join(home, "logs", "daemon.stderr.log")
	} else {
		s.Name = "openplexer-daemon-" + hash + ".service"
		config := os.Getenv("XDG_CONFIG_HOME")
		if !filepath.IsAbs(config) {
			config = filepath.Join(userHome, ".config")
		}
		s.path = filepath.Join(config, "systemd", "user", s.Name)
	}
	return s, nil
}

func canonicalServiceHome(path string) (string, error) {
	resolved, err := projects.CanonicalPath(path)
	if !errors.Is(err, os.ErrNotExist) {
		return resolved, err
	}
	// Status and uninstall also work after a home directory is deleted.
	path = filepath.Clean(path)
	if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		return "", err
	}
	parent, err := canonicalServiceHome(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

// LogLocation gives the files or journal command for this service.
func (s *Service) LogLocation() string {
	if s.platform == "darwin" {
		return s.stdout + "\n" + s.stderr
	}
	return "journalctl --user -u " + s.Name
}

// Install validates the configured home and enables the login daemon.
func (s *Service) Install(binary string) error {
	if !filepath.IsAbs(binary) {
		return errors.New("the installed openplexer executable path must be absolute")
	}
	cfg, err := readConfig(s.Home)
	if err != nil {
		return err
	}
	if _, err := cfg.client(); err != nil {
		return err
	}
	keys, err := readKeys(s.Home)
	if err != nil {
		return err
	}
	if keys == nil {
		return errors.New("configure the hub with a trusted key set (--keys)")
	}
	definition, err := s.definition(binary)
	if err != nil {
		return err
	}
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Close()

	ctx := context.Background()
	current, err := s.managerStatus(ctx)
	if err != nil {
		return err
	}
	activity, err := ReadDaemonActivity(s.Home)
	if err != nil {
		return err
	}
	if activity.PID != current.PID && activity.running() {
		if current.loaded {
			// The manager may have started its daemon since the first status check.
			current, err = s.managerStatus(ctx)
			if err != nil {
				return err
			}
		}
		if activity.PID != current.PID {
			return errors.New("stop the foreground openplexer daemon before installing the service for this home")
		}
	}
	if s.platform == "darwin" {
		if err := s.prepareLogs(); err != nil {
			return err
		}
	}
	installed, err := os.ReadFile(s.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !bytes.Equal(definition, installed) {
		if current.loaded {
			if err := s.stop(current); err != nil {
				return err
			}
			current = ServiceStatus{}
		}
		if err := writeFile(filepath.Dir(s.path), filepath.Base(s.path), definition); err != nil {
			return err
		}
	}
	if s.platform != "linux" || current.State != "running" || !current.enabled {
		if err := s.start(current); err != nil {
			return s.startError(err)
		}
	}
	return s.waitForStart()
}

func (s *Service) waitForStart() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	poll := time.NewTicker(50 * time.Millisecond)
	defer poll.Stop()
	failure := "the managed daemon did not acquire the Room lock"
	for ctx.Err() == nil {
		status, err := s.managerStatus(ctx)
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			return s.startError(err)
		}
		if status.Failure != "" {
			failure = status.Failure
		}
		if status.State == "running" {
			activity, err := ReadDaemonActivity(s.Home)
			if err != nil {
				return s.startError(err)
			}
			if activity.PID == status.PID {
				return nil
			}
		}
		select {
		case <-poll.C:
		case <-ctx.Done():
		}
	}
	return s.startError(errors.New(failure))
}

func (s *Service) startError(err error) error {
	return fmt.Errorf("could not start %s: %w\nlogs: %s\nIf a foreground daemon is running for this home, stop the foreground openplexer daemon and retry", s.Name, err, s.LogLocation())
}

// Uninstall stops and disables the daemon before removing its definition.
func (s *Service) Uninstall() error {
	lock, err := s.lock()
	if err == nil {
		defer lock.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ctx := context.Background()
	current, err := s.managerStatus(ctx)
	if err != nil {
		return err
	}
	present, err := s.hasDefinition()
	if err != nil || (!present && !current.loaded) {
		return err
	}
	if err := s.stop(current); err != nil {
		return err
	}
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if s.platform == "linux" {
		_, err = s.systemctl(ctx, "daemon-reload")
	}
	return err
}

// Status reports the service independently of the home policy's freshness.
func (s *Service) Status() (ServiceStatus, error) {
	status, err := s.managerStatus(context.Background())
	if err == nil && status.State == "running" {
		activity, _ := ReadDaemonActivity(s.Home)
		if activity.PID == status.PID {
			status.Activity = activity
		}
	}
	if err != nil || status.loaded {
		return status, err
	}
	present, err := s.hasDefinition()
	if present {
		status.State = "stopped"
	}
	return status, err
}

func (s *Service) hasDefinition() (bool, error) {
	_, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (s *Service) lock() (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(s.Home, "service.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFileExclusive(file); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
