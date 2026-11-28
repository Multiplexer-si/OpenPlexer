//go:build !unix

package daemon

import (
	"context"
	"errors"
	"os"
)

func lockFileExclusive(*os.File) error {
	return errors.ErrUnsupported
}

func tryLockFileExclusive(*os.File) error {
	return errors.ErrUnsupported
}

func lockFileContext(context.Context, *os.File) error { return errors.ErrUnsupported }
