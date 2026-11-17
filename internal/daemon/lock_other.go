//go:build !unix

package daemon

import (
	"errors"
	"os"
)

func lockFileExclusive(*os.File) error {
	return errors.ErrUnsupported
}
