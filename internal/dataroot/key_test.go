package dataroot_test

import (
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/dataroot"
)

func TestKeyIsStableForEquivalentPaths(t *testing.T) {
	root := t.TempDir()
	first, err := dataroot.Key(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := dataroot.Key(filepath.Join(root, "."))
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != 16 {
		t.Fatalf("keys = %q %q", first, second)
	}
}
