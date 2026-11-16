package workspacefiles

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestReadDocumentIsBoundedAndDoesNotFollowLinks(t *testing.T) {
	root := t.TempDir()
	pdf := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), MaxBytes+1)...)
	if err := os.WriteFile(filepath.Join(root, "report.PDF"), pdf, 0600); err != nil {
		t.Fatal(err)
	}
	got, digest, err := ReadDocument(root, "report.PDF")
	sum := sha256.Sum256(pdf)
	if err != nil || !bytes.Equal(got, pdf) || digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("PDF snapshot: %d %s %v", len(got), digest, err)
	}
	if err := os.WriteFile(filepath.Join(root, "fake.pdf"), []byte("<html>not PDF</html>"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "report.PDF"), filepath.Join(root, "link.pdf")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.pdf"), pdf, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory.pdf"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "fifo.pdf"), 0600); err != nil {
		t.Fatal(err)
	}
	big, err := os.Create(filepath.Join(root, "huge.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(MaxDocumentBytes + 1); err != nil {
		t.Fatal(err)
	}
	big.Close()
	for _, p := range []string{"fake.pdf", "report.html", "link.pdf", "linked-dir/secret.pdf", "directory.pdf", "fifo.pdf", "huge.pdf", "../secret.pdf", ".git/secret.pdf", "a/../report.PDF", filepath.Join(outside, "secret.pdf")} {
		if data, _, err := ReadDocument(root, p); err == nil || len(data) != 0 {
			t.Errorf("unsafe document %q: %d bytes %v", p, len(data), err)
		}
	}
	for _, p := range []string{"missing.pdf", "missing/report.pdf"} {
		if _, _, err := ReadDocument(root, p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("missing %s: %v", p, err)
		}
	}
}
