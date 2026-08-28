package testutil

import (
	"os"
	"testing"
)

// ShortTempDir creates a temporary directory suitable for Unix socket paths.
// macOS has a much smaller sockaddr_un path limit than Linux.
func ShortTempDir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "wi-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
