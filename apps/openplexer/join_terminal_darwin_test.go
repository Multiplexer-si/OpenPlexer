package main

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"
)

func openJoinTestTerminal(master *os.File) (*os.File, error) {
	if err := joinTerminalIoctl(master, syscall.TIOCPTYGRANT, nil); err != nil {
		return nil, err
	}
	if err := joinTerminalIoctl(master, syscall.TIOCPTYUNLK, nil); err != nil {
		return nil, err
	}
	var path [128]byte
	if err := joinTerminalIoctl(master, syscall.TIOCPTYGNAME, unsafe.Pointer(&path)); err != nil {
		return nil, err
	}
	return os.OpenFile(string(bytes.TrimRight(path[:], "\x00")), os.O_RDWR|syscall.O_NOCTTY, 0)
}
