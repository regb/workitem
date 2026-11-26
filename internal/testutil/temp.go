package testutil

import (
	"os"
	"testing"
)

// ShortTempDir creates a private temporary directory, removed when the test
// ends, whose path is short enough for Unix socket paths. macOS limits
// sockaddr_un paths to 104 bytes, which t.TempDir() under the default TMPDIR
// and a long test name easily exceeds. Bind test sockets here, never under
// t.TempDir().
//
// Under RunIsolatedTests the directory lives inside the isolated test root, so
// it is removed with that root and never touches the operator's runtime
// directories; otherwise it is created under /tmp.
func ShortTempDir(t testing.TB) string {
	t.Helper()
	parent, prefix := "/tmp", "wi-"
	if root, ok := isolatedRoot(); ok {
		parent, prefix = root, "s"
	}
	dir, err := os.MkdirTemp(parent, prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
