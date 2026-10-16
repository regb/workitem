package direnv

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

func snapshot(path string) ([32]byte, error) {
	var zero [32]byte
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return zero, fmt.Errorf("inspect regular worktree .envrc before approval")
	}
	f, err := os.Open(path)
	if err != nil {
		return zero, fmt.Errorf("read worktree .envrc before approval")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return zero, fmt.Errorf("worktree .envrc changed before approval")
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return zero, fmt.Errorf("worktree .envrc exceeds approval read limit or cannot be read")
	}
	return sha256.Sum256(data), nil
}
