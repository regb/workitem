//go:build linux

package tmux

import (
	"os"
	"testing"
)

func TestIsTerminalFileRejectsRegularFile(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "not-a-terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if isTerminalFile(file) {
		t.Fatal("regular file reported as terminal")
	}
}
