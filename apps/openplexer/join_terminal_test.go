//go:build darwin || linux

package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

type joinPrompt struct {
	buffer bytes.Buffer
	ready  chan struct{}
}

func (p *joinPrompt) Write(data []byte) (int, error) {
	n, err := p.buffer.Write(data)
	if strings.Contains(string(data), "Paste the private invite link") {
		close(p.ready)
	}
	return n, err
}

func TestJoinHiddenTerminalInput(t *testing.T) {
	c, _, _, code := joinCLI(t)
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skip("PTY allocation is unavailable")
	}
	defer master.Close()
	slave, err := openJoinTestTerminal(master)
	if err != nil {
		t.Fatal(err)
	}
	defer slave.Close()
	var before syscall.Termios
	if err := joinTerminalIoctl(slave, joinGetTermios, unsafe.Pointer(&before)); err != nil {
		t.Fatal(err)
	}
	before.Lflag |= syscall.ICANON | syscall.ECHO | syscall.ECHONL
	if err := joinTerminalIoctl(slave, joinSetTermios, unsafe.Pointer(&before)); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	prompt := &joinPrompt{ready: make(chan struct{})}
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"join", "--install-hook"}, slave, &out, prompt, func() time.Time { return c.now })
	}()
	select {
	case <-prompt.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("the hidden prompt did not appear")
	}
	var hidden syscall.Termios
	if err := joinTerminalIoctl(slave, joinGetTermios, unsafe.Pointer(&hidden)); err != nil || hidden.Lflag&(syscall.ECHO|syscall.ECHONL) != 0 {
		t.Fatal("the terminal echoes invite input")
	}
	if _, err := io.WriteString(master, c.stdin+"n\n"); err != nil {
		t.Fatal("could not supply the invite to the PTY")
	}
	select {
	case status := <-done:
		if status != exitOK {
			t.Fatalf("interactive join exited %d", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interactive join did not finish")
	}
	var after syscall.Termios
	if err := joinTerminalIoctl(slave, joinGetTermios, unsafe.Pointer(&after)); err != nil || after.Lflag != before.Lflag {
		t.Fatal("interactive join did not restore terminal echo")
	}
	output := out.String() + prompt.buffer.String()
	if strings.Contains(output, code) || !strings.Contains(output, "policy: verified") ||
		!strings.Contains(output, "To start the login service") || strings.Contains(output, "installed service") {
		t.Fatal("interactive join leaked the link, omitted the policy result, or missed declined consent")
	}
}
