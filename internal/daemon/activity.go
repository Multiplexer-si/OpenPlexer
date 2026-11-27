package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	daemonPIDFile     = "daemon.pid"
	heartbeatInterval = 5 * time.Second
)

// DaemonActivity records when the daemon acquired the Room lock and last checked in.
type DaemonActivity struct {
	PID         int       `json:"pid"`
	StartedAt   time.Time `json:"started_at"`
	HeartbeatAt time.Time `json:"heartbeat_at"`
}

// ReadDaemonActivity reads local daemon activity without taking a lock.
// A missing or invalid record returns zero activity.
func ReadDaemonActivity(home string) (DaemonActivity, error) {
	data, err := os.ReadFile(filepath.Join(home, daemonPIDFile))
	if errors.Is(err, os.ErrNotExist) {
		return DaemonActivity{}, nil
	}
	if err != nil {
		return DaemonActivity{}, err
	}
	var activity DaemonActivity
	if json.Unmarshal(data, &activity) != nil || activity.PID <= 0 ||
		activity.StartedAt.IsZero() || activity.HeartbeatAt.IsZero() {
		return DaemonActivity{}, nil
	}
	return activity, nil
}

func (a DaemonActivity) running() bool {
	age := time.Since(a.HeartbeatAt)
	if a.PID == 0 || age < 0 || age > 3*heartbeatInterval {
		return false
	}
	process, err := os.FindProcess(a.PID)
	if err != nil {
		return false
	}
	defer process.Release()
	return process.Signal(syscall.Signal(0)) == nil
}

func (a DaemonActivity) heartbeat(ctx context.Context, home string, report func(error)) {
	tick := time.NewTicker(heartbeatInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			a.HeartbeatAt = time.Now().UTC()
			if err := writeJSON(home, daemonPIDFile, a); err != nil {
				report(fmt.Errorf("update daemon heartbeat: %w", err))
			}
		}
	}
}
