//go:build linux

package tmux

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

func interactiveStdio() (stdin, stdout, stderr *os.File, cleanup func(), err error) {
	for _, file := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
		if !isTerminalFile(file) {
			continue
		}
		path, err := os.Readlink(filepath.Join("/proc/self/fd", fmt.Sprint(file.Fd())))
		if err != nil || !filepath.IsAbs(path) {
			continue
		}
		// Reopen the concrete terminal device even when stdin is already a TTY.
		// Passing Go's inherited descriptor can make older tmux servers reject
		// the SCM_RIGHTS descriptor as non-terminal. Tmux also rejects /dev/tty
		// itself, whereas the resolved /dev/pts/N device is accepted.
		tty, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			continue
		}
		return tty, tty, tty, func() { _ = tty.Close() }, nil
	}
	return nil, nil, nil, func() {}, fmt.Errorf("interactive tmux attachment requires a terminal on stdin, stdout, or stderr; use --json for non-interactive execution")
}

func isTerminalFile(file *os.File) bool {
	if file == nil {
		return false
	}
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&termios)))
	return errno == 0
}
