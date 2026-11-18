//go:build unix

package hook

import (
	"fmt"
	"os"
	"syscall"
)

// lockDir blocks until this process holds the lock on dir. The end of the
// process releases it, so a run that is killed does not block the next one.
func lockDir(dir string) (unlock func(), err error) {
	file, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, fmt.Errorf("cannot lock %s: %w", dir, err)
	}
	return func() { file.Close() }, nil
}
