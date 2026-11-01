package repofiles_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/repofiles"
)

func TestFilesAreStoredPrivatelyByPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "files")
	if _, err := repofiles.Put(dir, ".env", []byte("A=1\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := repofiles.Put(dir, "certs/dev.pem", []byte("pem")); err != nil {
		t.Fatal(err)
	}
	if _, err := repofiles.Put(dir, ".env", []byte("A=2\n")); err != nil {
		t.Fatal(err)
	}
	files, err := repofiles.List(dir)
	if err != nil || len(files) != 2 || files[0].Path != ".env" || files[0].Size != 4 || files[1].Path != "certs/dev.pem" {
		t.Fatalf("files = %+v, %v", files, err)
	}
	info, err := os.Stat(filepath.Join(dir, ".env"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", info, err)
	}
	if content, err := repofiles.Read(dir, ".env"); err != nil || string(content) != "A=2\n" {
		t.Fatalf("content = %q, %v", content, err)
	}
	if err := repofiles.Remove(dir, "certs/dev.pem"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "certs")); !os.IsNotExist(err) {
		t.Fatalf("empty directory kept: %v", err)
	}
	if _, err := repofiles.Put(dir, ".env/x", []byte("x")); err == nil {
		t.Fatal("a file path inside another file was accepted")
	}
	if files, err := repofiles.List(filepath.Join(dir, "missing")); err != nil || len(files) != 0 {
		t.Fatalf("missing dir = %v, %v", files, err)
	}
}

func TestPathsStayInsideTheCheckout(t *testing.T) {
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../b", ".git/config", "a/.git/x", "a//b", "./a", "a\\b"} {
		if repofiles.ValidPath(bad) == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	for _, good := range []string{".env", "config/local.toml", ".envrc.local"} {
		if err := repofiles.ValidPath(good); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
}
