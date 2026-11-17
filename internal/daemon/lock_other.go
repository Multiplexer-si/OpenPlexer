//go:build !unix

package daemon

import (
	"errors"
	"os"
)

// lockFileExclusive has no implementation outside Unix yet, so a load and
// an audit record fail there with this error.
func lockFileExclusive(*os.File) error {
	return errors.ErrUnsupported
}
