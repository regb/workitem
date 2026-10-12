package pi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoteWorkspaceExtensionPathMaterializesBundle(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path, err := RemoteWorkspaceExtensionPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "index.ts" {
		t.Fatalf("path=%q", path)
	}
	for _, name := range []string{"index.ts"} {
		info, err := os.Stat(filepath.Join(filepath.Dir(path), name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode=%o", name, info.Mode().Perm())
		}
	}
}
