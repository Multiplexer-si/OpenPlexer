//go:build darwin || linux

package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestServiceInstallRejectsInvalidRoomCursor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	serviceTestHome(t, home)
	cursor := filepath.Join(home, roomCursorFile)
	if err := syscall.Mkfifo(cursor, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	var started bool
	var polls int
	var writer *os.File
	runner := serviceRunnerFunc(func(ctx context.Context, _ string, args ...string) ([]byte, error) {
		if args[1] == "enable" {
			started = true
			go func() { done <- Run(t.Context(), home, time.Now, nil, func(error) {}) }()
			return nil, nil
		}
		if !started {
			return []byte("LoadState=not-found\n"), nil
		}
		polls++
		switch polls {
		case 1:
			// Wait until the daemon opens the cursor, then hold the read open for the first status check.
			for {
				f, err := os.OpenFile(cursor, os.O_WRONLY|syscall.O_NONBLOCK, 0)
				if err == nil {
					writer = f
					t.Cleanup(func() { f.Close() })
					break
				}
				if !errors.Is(err, syscall.ENXIO) {
					t.Fatal(err)
				}
				select {
				case err := <-done:
					t.Fatalf("daemon exited before reading the cursor: %v", err)
				case <-ctx.Done():
					t.Fatal("daemon did not open the cursor")
				case <-time.After(time.Millisecond):
				}
			}
			return fmt.Appendf(nil, "LoadState=loaded\nActiveState=active\nMainPID=%d\n", os.Getpid()), nil
		case 2:
			if _, err := writer.WriteString(`{"last_seq":-1}`); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
		}
		return []byte("LoadState=loaded\nActiveState=failed\nResult=exit-code\nExecMainStatus=1\n"), nil
	})
	s, err := newService(home, "linux", runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Install("/opt/openplexer"); err == nil || !strings.Contains(err.Error(), s.LogLocation()) {
		t.Fatalf("install should fail with the log location for an invalid cursor, got %v", err)
	}
	select {
	case err := <-done:
		if err == nil || err.Error() != "invalid stored Room cursor" {
			t.Fatalf("daemon did not reject the invalid cursor: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon did not return after reading the invalid cursor")
	}
}
