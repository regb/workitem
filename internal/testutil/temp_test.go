package testutil

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShortTempDirStaysInsideTheIsolatedRoot(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "wi-tests-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv(testProcessRoot, root)
	var dir string
	t.Run("a test name long enough to push t.TempDir() past the macOS socket path limit", func(t *testing.T) {
		dir = ShortTempDir(t)
		if filepath.Dir(dir) != root {
			t.Fatalf("directory %q is outside the isolated root %q", dir, root)
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			t.Fatalf("directory %v err=%v", info, err)
		}
		listener, err := net.Listen("unix", filepath.Join(dir, strings.Repeat("s", 40)+".sock"))
		if err != nil {
			t.Fatal(err)
		}
		_ = listener.Close()
	})
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("directory survived its test: %v", err)
	}
}

func TestShortTempDirOutsideIsolationUsesTmp(t *testing.T) {
	t.Setenv(testProcessRoot, "")
	dir := ShortTempDir(t)
	if filepath.Dir(dir) != "/tmp" || !strings.HasPrefix(filepath.Base(dir), "wi-") {
		t.Fatalf("directory %q", dir)
	}
}
