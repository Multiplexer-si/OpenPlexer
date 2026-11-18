//go:build unix

package hub

import (
	"errors"
	"os"
	"syscall"
)

func lockExclusive(file *os.File) error {
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}
