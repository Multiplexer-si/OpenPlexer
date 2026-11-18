//go:build !unix

package hook

func lockDir(string) (unlock func(), err error) {
	return func() {}, nil
}
