//go:build !unix

package daemon

import (
	"errors"
	"os"
)

func lockFileExclusive(*os.File) error {
	return errors.ErrUnsupported
}

func tryLockFileExclusive(*os.File) error {
	return errors.ErrUnsupported
}
