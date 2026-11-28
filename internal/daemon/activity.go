package daemon

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	daemonPIDFile     = "daemon.pid"
	heartbeatInterval = 5 * time.Second
	daemonForeground  = "foreground"
	daemonManaged     = "service"
)

// DaemonActivity records when the daemon acquired the Room lock and last checked in.
type DaemonActivity struct {
	PID         int       `json:"pid"`
	StartedAt   time.Time `json:"started_at"`
	HeartbeatAt time.Time `json:"heartbeat_at"`
	Mode        string    `json:"mode,omitempty"`
}

// ReadDaemonActivity reads local daemon activity without taking a lock.
// A missing or invalid record returns zero activity.
func ReadDaemonActivity(home string) (DaemonActivity, error) {
	activity, file, err := openDaemonActivity(home)
	if file != nil {
		file.Close()
	}
	return activity, err
}

func openDaemonActivity(home string) (DaemonActivity, *os.File, error) {
	file, err := os.Open(filepath.Join(home, daemonPIDFile))
	if errors.Is(err, os.ErrNotExist) {
		return DaemonActivity{}, nil, nil
	}
	if err != nil {
		return DaemonActivity{}, nil, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		file.Close()
		return DaemonActivity{}, nil, err
	}
	var activity DaemonActivity
	if jsonv2.Unmarshal(data, &activity, jsonv2.RejectUnknownMembers(true)) != nil || activity.PID <= 0 ||
		activity.StartedAt.IsZero() || activity.HeartbeatAt.IsZero() ||
		(activity.Mode != "" && activity.Mode != daemonForeground && activity.Mode != daemonManaged) {
		file.Close()
		return DaemonActivity{}, nil, nil
	}
	return activity, file, nil
}

// publish locks each new activity file before making it visible. A reader
// holding the file open can tell when this publication has lost its owner,
// even if the process PID has been reused.
func (a DaemonActivity) publish(home string) (*os.File, error) {
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return nil, err
	}
	var lease *os.File
	if err := replaceFile(home, daemonPIDFile, append(data, '\n'), func(file *os.File) error {
		lease = file
		return tryLockFileExclusive(file)
	}); err != nil {
		return nil, err
	}
	return lease, nil
}

func (a DaemonActivity) heartbeatFresh() bool {
	age := time.Since(a.HeartbeatAt)
	return age >= 0 && age <= 3*heartbeatInterval
}

func (a DaemonActivity) running() bool {
	if a.PID == 0 || !a.heartbeatFresh() {
		return false
	}
	process, err := os.FindProcess(a.PID)
	if err != nil {
		return false
	}
	defer process.Release()
	return process.Signal(syscall.Signal(0)) == nil
}

func (a DaemonActivity) heartbeat(ctx context.Context, home string, lease *os.File, report func(error)) {
	tick := time.NewTicker(heartbeatInterval)
	defer tick.Stop()
	defer func() { lease.Close() }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			a.HeartbeatAt = time.Now().UTC()
			next, err := a.publish(home)
			if err != nil {
				report(fmt.Errorf("update daemon heartbeat: %w", err))
			} else {
				lease.Close()
				lease = next
			}
		}
	}
}
