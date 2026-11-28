//go:build !darwin && !linux

package main

import (
	"bufio"
	"errors"
	"io"
	"os"
)

func readInviteFromTerminal(*os.File, *bufio.Reader, io.Writer) (string, error) {
	return "", errors.New("hidden terminal input is unavailable; use --link-stdin to read a private file")
}
