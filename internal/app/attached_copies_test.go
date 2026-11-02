package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/itemnotes"
	"github.com/regb/workitem/internal/model"
)

func TestReleaseRemovesOnlyUnchangedAttachedCopies(t *testing.T) {
	a, st, m := newCoreTestApp(t, time.Now(), model.StateActive, false)
	a.Git = gitpkg.New("")
	checkout := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", checkout}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init")
	m.Checkout.Claim = &model.WorkspaceClaim{Path: checkout}
	write := func(p, content string) {
		t.Helper()
		full := filepath.Join(checkout, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	attach := func(name, path, content string) {
		t.Helper()
		dir := st.ItemDir(m.ID)
		digest, err := itemnotes.WriteBlob(dir, []byte(content))
		if err != nil {
			t.Fatal(err)
		}
		note := itemnotes.Note{Time: time.Now(), Text: "Attached " + name, Attachment: &itemnotes.Attachment{Name: name, SHA256: digest, Size: int64(len(content)), Type: "text/markdown", Path: path}}
		if err := itemnotes.Append(dir, note); err != nil {
			t.Fatal(err)
		}
	}
	// A tracked file changed: uncommitted, never an attached copy.
	write("tracked.md", "one")
	run("add", "tracked.md")
	run("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "tracked")
	write("tracked.md", "two")
	// Attached under another name from a new folder: removed, folder too.
	write("doc/tmp/X.md", "design")
	attach("DESIGN.md", "doc/tmp/X.md", "design")
	// Edited since attached: kept.
	write("notes.md", "edited")
	attach("notes.md", "notes.md", "original")
	// Same content as an attachment, but not attached from here: kept.
	write("copy.md", "design")
	// Attached twice; the latest attachment from the path decides.
	write("plan.md", "v1")
	attach("plan.md", "plan.md", "v1")
	attach("plan.md", "plan.md", "v2")
	// Attached from outside the checkout: no path, nothing to remove.
	attach("outside.md", "", "design")

	view, err := a.uncommittedChanges(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(view.Files)
	if !slices.Equal(view.Attached, []string{"doc/tmp/X.md"}) || !slices.Equal(view.Files, []string{"copy.md", "notes.md", "plan.md", "tracked.md"}) {
		t.Fatalf("uncommitted %+v", view)
	}

	removed, err := a.removeAttachedCopies(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 {
		t.Fatalf("removed %v", removed)
	}
	if _, err := os.Stat(filepath.Join(checkout, "doc")); !os.IsNotExist(err) {
		t.Fatalf("doc folder left behind: %v", err)
	}
	left, err := a.Git.UntrackedFiles(context.Background(), checkout)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(left)
	if !slices.Equal(left, []string{"copy.md", "notes.md", "plan.md"}) {
		t.Fatalf("left %v", left)
	}
	if _, err := a.ensureCheckoutReleasable(context.Background(), m); err == nil {
		t.Fatal("released a checkout with uncommitted files")
	}
}
