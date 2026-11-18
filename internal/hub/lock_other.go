//go:build !unix

package hub

import (
	"errors"
	"os"
)

func lockExclusive(*os.File) error { return errors.ErrUnsupported }
