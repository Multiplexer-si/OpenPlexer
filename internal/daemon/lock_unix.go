//go:build unix

package daemon

import (
	"os"
	"syscall"
)

// lockFileExclusive blocks until this process holds the lock. Closing the
// file releases it, and so does the end of the process.
func lockFileExclusive(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}
