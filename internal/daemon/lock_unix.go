//go:build unix

package daemon

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

// lockFileExclusive blocks until this process holds the lock. Closing the
// file releases it, and so does the end of the process.
func lockFileExclusive(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}

func tryLockFileExclusive(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func tryLockFileShared(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
}

func lockFileContext(ctx context.Context, file *os.File) error {
	if ctx.Done() == nil {
		return lockFileExclusive(file)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for ctx.Err() == nil {
		err := tryLockFileExclusive(file)
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			return err
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
	return ctx.Err()
}
