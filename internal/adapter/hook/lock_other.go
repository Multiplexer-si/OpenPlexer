//go:build !unix

package hook

import "errors"

func lockDir(string) (unlock func(), err error) {
	return nil, errors.ErrUnsupported
}
