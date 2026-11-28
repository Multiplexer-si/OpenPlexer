package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

func openJoinTestTerminal(master *os.File) (*os.File, error) {
	var lock int32
	if err := joinTerminalIoctl(master, syscall.TIOCSPTLCK, unsafe.Pointer(&lock)); err != nil {
		return nil, err
	}
	var dev uint32
	if err := joinTerminalIoctl(master, syscall.TIOCGPTN, unsafe.Pointer(&dev)); err != nil {
		return nil, err
	}
	return os.OpenFile(fmt.Sprintf("/dev/pts/%d", dev), os.O_RDWR|syscall.O_NOCTTY, 0)
}
