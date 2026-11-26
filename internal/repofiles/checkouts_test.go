package repofiles_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/repofiles"
	"github.com/regb/workitem/internal/testutil"
)

func TestRemovingAFileCleansCheckoutsNotInUse(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := testutil.GitStore(t, root, testutil.GitBranch{Name: "main"})
	idle, busy, edited := filepath.Join(root, "slot-1"), filepath.Join(root, "slot-2"), filepath.Join(root, "slot-3")
	for i, slot := range []string{idle, busy, edited} {
		testutil.GitWorktree(t, store, slot, "main", "-b", "b"+string(rune('1'+i)))
	}
	files := filepath.Join(root, "files")
	if _, err := repofiles.Put(files, ".env", []byte("A=1\n")); err != nil {
		t.Fatal(err)
	}
	for _, slot := range []string{idle, busy, edited} {
		if _, err := repofiles.CopyInto(ctx, slot, files, "claim", nil, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repofiles.UpdateExclude(ctx, store, files); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(edited, ".env"), []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := repofiles.Remove(files, ".env"); err != nil {
		t.Fatal(err)
	}
	warnings, err := repofiles.Sync(ctx, store, files, map[string]bool{busy: true})
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "edited") {
		t.Fatalf("warnings=%v err=%v", warnings, err)
	}
	exists := func(slot string) bool { _, err := os.Stat(filepath.Join(slot, ".env")); return err == nil }
	if exists(idle) || !exists(busy) || !exists(edited) {
		t.Fatalf("idle=%v busy=%v edited=%v", exists(idle), exists(busy), exists(edited))
	}
	// The copy left in use stays ignored; the edited one is the operator's now.
	exclude, _ := os.ReadFile(filepath.Join(store, "info", "exclude"))
	if !strings.Contains(string(exclude), "/.env") {
		t.Fatalf("exclude =\n%s", exclude)
	}
	// Its next claim drops the copy, and nothing is ignored any more.
	if _, err := repofiles.CopyInto(ctx, busy, files, "next", nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(edited, ".env")); err != nil {
		t.Fatal(err)
	}
	if err := repofiles.UpdateExclude(ctx, store, files); err != nil {
		t.Fatal(err)
	}
	exclude, _ = os.ReadFile(filepath.Join(store, "info", "exclude"))
	if exists(busy) || strings.Contains(string(exclude), ".env") {
		t.Fatalf("busy=%v exclude=\n%s", exists(busy), exclude)
	}
}
