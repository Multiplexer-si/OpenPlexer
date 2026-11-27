//go:build !unix

package projects

import (
	"errors"
	"io/fs"
	"os"
)

func ownedWithMode(fs.FileInfo, fs.FileMode) bool { return false }

func openRegistry(string) (*os.File, error) { return nil, errors.ErrUnsupported }

func lockRegistry(*os.File) error { return errors.ErrUnsupported }
