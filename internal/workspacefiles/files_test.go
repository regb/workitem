package workspacefiles

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSaveChecksContentAndPreservesMode(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.go")
	if err := os.WriteFile(p, []byte("old\n"), 0755); err != nil {
		t.Fatal(err)
	}
	old, err := Read(root, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := Save(root, "a.go", "new\n", old.SHA256)
	if err != nil || saved.Content != "new\n" {
		t.Fatalf("save: %+v %v", saved, err)
	}
	if _, err = Save(root, "a.go", "stale\n", old.SHA256); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale save: %v", err)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0755 {
		t.Fatalf("mode: %v", info.Mode())
	}
	files, _ := os.ReadDir(root)
	if len(files) != 1 {
		t.Fatal("temporary save file leaked")
	}
}
func TestPathsAndSpecialFilesAreRefused(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".git", filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../secret", "/etc/passwd", "a/../../secret", ".git/config", "a/.GIT/config", "escape/secret", "alias/config", ".git", "."} {
		if _, err := Read(root, p); err == nil {
			t.Errorf("read accepted %q", p)
		}
		if _, err := Save(root, p, "replacement", "digest"); err == nil {
			t.Errorf("save accepted %q", p)
		}
	}
	if _, err := Read(root, "escape"); err == nil {
		t.Error("read followed file symlink")
	}
}
func TestMissingCheckoutIsNotReportedAsADeletedFile(t *testing.T) {
	root := t.TempDir()
	if _, err := Read(filepath.Join(root, "absent-checkout"), "a.go"); err == nil {
		t.Fatal("missing checkout was reported as a missing source file")
	}
	file, err := Read(root, "absent-directory/a.go")
	if err != nil || file.Exists {
		t.Fatalf("missing nested file: %+v %v", file, err)
	}
}

func TestFIFOsAreRefusedWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root, "pipe"); err == nil {
		t.Fatal("FIFO accepted as source")
	}
	if _, err := Save(root, "pipe", "text", "digest"); err == nil {
		t.Fatal("FIFO accepted for save")
	}
}

func TestBinaryOversizedMissingAndEmptySource(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name, content string
		binary, large bool
	}{{"empty", "", false, false}, {"binary", "\x00abc", true, false}, {"utf8", "\xff", true, false}, {"large", strings.Repeat("x", MaxBytes+1), false, true}} {
		if err := os.WriteFile(filepath.Join(root, tc.name), []byte(tc.content), 0600); err != nil {
			t.Fatal(err)
		}
		f, err := Read(root, tc.name)
		if err != nil || !f.Exists || f.Binary != tc.binary || f.TooLarge != tc.large {
			t.Fatalf("%s: %+v %v", tc.name, f, err)
		}
		if (tc.binary || tc.large) && f.Content != "" {
			t.Fatal("unsafe contents returned")
		}
	}
	f, err := Read(root, "missing")
	if err != nil || f.Exists {
		t.Fatalf("missing: %+v %v", f, err)
	}
	if _, err := Save(root, "new", "new", Source("new", nil).SHA256); !errors.Is(err, ErrConflict) {
		t.Fatalf("created missing file: %v", err)
	}
}
