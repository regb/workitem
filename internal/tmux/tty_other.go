//go:build !linux

package tmux

import (
	"fmt"
	"os"
)

func interactiveStdio() (stdin, stdout, stderr *os.File, cleanup func(), err error) {
	if info, statErr := os.Stdin.Stat(); statErr == nil && info.Mode()&os.ModeCharDevice != 0 {
		return os.Stdin, os.Stdout, os.Stderr, func() {}, nil
	}
	return nil, nil, nil, func() {}, fmt.Errorf("interactive tmux attachment requires a terminal on stdin; use --json for non-interactive execution")
}
