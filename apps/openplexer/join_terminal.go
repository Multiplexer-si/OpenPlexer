//go:build darwin || linux

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

func joinTerminalIoctl(file *os.File, request uint, pointer unsafe.Pointer) error {
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var ioctlErr syscall.Errno
	err = raw.Control(func(fd uintptr) {
		_, _, ioctlErr = syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(request), uintptr(pointer))
	})
	if err != nil {
		return err
	}
	if ioctlErr != 0 {
		return ioctlErr
	}
	return nil
}

func readInviteFromTerminal(file *os.File, input *bufio.Reader, prompt io.Writer) (string, error) {
	useStdin := errors.New("join needs a terminal for hidden input; use --link-stdin to read a private file")
	var before syscall.Termios
	if file == nil || joinTerminalIoctl(file, joinGetTermios, unsafe.Pointer(&before)) != nil {
		return "", useStdin
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	hidden := before
	hidden.Lflag &^= syscall.ECHO | syscall.ECHONL
	if err := joinTerminalIoctl(file, joinSetTermios, unsafe.Pointer(&hidden)); err != nil {
		return "", useStdin
	}

	fmt.Fprint(prompt, "Paste the private invite link: ")
	type read struct {
		line string
		err  error
	}
	line := make(chan read, 1)
	go func() {
		text, err := input.ReadSlice('\n')
		line <- read{string(text), err}
	}()
	var result read
	select {
	case result = <-line:
	case <-ctx.Done():
		result.err = ctx.Err()
	}
	restoreErr := joinTerminalIoctl(file, joinSetTermios, unsafe.Pointer(&before))
	fmt.Fprintln(prompt)
	if restoreErr != nil {
		return "", errors.New("could not restore terminal settings")
	}
	if result.err != nil || len(result.line) > maxInviteLink {
		return "", errors.New("could not read the invite link")
	}
	return result.line, nil
}
