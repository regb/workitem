package coordinator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRepositorySyncIgnoresExternalWorktrees(t *testing.T) {
	for _, kind := range []string{"dirty", "active-operation", "missing", "slot-name-outside-wi", "other-repository-slot", "non-slot-in-wi-directory", "nested-worktree"} {
		t.Run(kind, func(t *testing.T) {
			f := newSyncFixture(t)
			base := f.status(false).LocalSHA
			external := filepath.Join(t.TempDir(), "review")
			switch kind {
			case "slot-name-outside-wi":
				external = filepath.Join(t.TempDir(), "slot-0001")
			case "other-repository-slot":
				external = filepath.Join(f.root, "worktrees", "another-repository", "slot-0001")
			case "non-slot-in-wi-directory":
				external = filepath.Join(f.root, "worktrees", importTestID, "review")
			case "nested-worktree":
				external = filepath.Join(f.root, "worktrees", importTestID, "slot-0001", "review")
			}
			syncGit(t, f.store, "worktree", "add", "--detach", external, base)
			data := []byte("private external worktree data\n")
			if err := os.WriteFile(filepath.Join(external, "document"), data, 0600); err != nil {
				t.Fatal(err)
			}
			syncGit(t, external, "add", "document")
			if err := os.WriteFile(filepath.Join(external, "dataset.csv"), data, 0600); err != nil {
				t.Fatal(err)
			}
			index := syncGit(t, external, "ls-files", "--stage")
			marker := ""
			if kind == "active-operation" {
				marker = syncGit(t, external, "rev-parse", "--git-path", "rebase-merge")
				if !filepath.IsAbs(marker) {
					marker = filepath.Join(external, marker)
				}
				if err := os.Mkdir(marker, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "missing" {
				if err := os.RemoveAll(external); err != nil {
					t.Fatal(err)
				}
			}
			registrations := syncGit(t, f.store, "worktree", "list", "--porcelain")
			remote := f.commit(f.origin, "remote", "remote change\n", "advance origin")
			result, err := f.sync("ff")
			if err != nil || !result.Changed || result.LocalSHA != remote {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if after := syncGit(t, f.store, "worktree", "list", "--porcelain"); after != registrations {
				t.Fatal("external Git registration was changed")
			}
			if kind == "missing" {
				if _, err := os.Stat(external); !os.IsNotExist(err) {
					t.Fatal("external checkout was recreated", err)
				}
				return
			}
			if got := syncGit(t, external, "rev-parse", "HEAD"); got != base {
				t.Fatal("external HEAD changed")
			}
			if got := syncGit(t, external, "ls-files", "--stage"); got != index {
				t.Fatal("external index changed")
			}
			for _, file := range []string{"document", "dataset.csv"} {
				after, err := os.ReadFile(filepath.Join(external, file))
				if err != nil || string(after) != string(data) {
					t.Fatal("external files changed", file, err)
				}
			}
			if marker != "" {
				if _, err := os.Stat(marker); err != nil {
					t.Fatal("external Git operation was altered", err)
				}
			}
		})
	}
}

func TestRepositoryReconciliationIgnoresExternalOperationsOnContinue(t *testing.T) {
	f := newSyncFixture(t)
	f.localCommit("document", "local\n", "local change")
	f.commit(f.origin, "document", "remote\n", "remote change")
	if _, err := f.sync("merge"); err == nil {
		t.Fatal("expected reconciliation conflict")
	}
	pending := f.status(false)
	external := filepath.Join(t.TempDir(), "external-review")
	syncGit(t, f.store, "worktree", "add", "--detach", external, "main")
	marker := syncGit(t, external, "rev-parse", "--git-path", "CHERRY_PICK_HEAD")
	if !filepath.IsAbs(marker) {
		marker = filepath.Join(external, marker)
	}
	old := syncGit(t, external, "rev-parse", "HEAD")
	if err := os.WriteFile(marker, []byte(old+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	resolved := f.commit(pending.ReconciliationPath, "document", "resolved\n", "resolve conflict")
	result, err := f.sync("continue")
	if err != nil || result.LocalSHA != resolved || result.Pending != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got := syncGit(t, external, "rev-parse", "HEAD"); got != old {
		t.Fatal("external HEAD changed")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != old+"\n" {
		t.Fatal("external operation changed", err)
	}
}

func TestRepositorySyncStillChecksMissingOwnedSlot(t *testing.T) {
	f := newSyncFixture(t)
	base := f.status(false).LocalSHA
	slot := filepath.Join(f.root, "worktrees", importTestID, "slot-0001")
	syncGit(t, f.store, "worktree", "add", "--detach", slot, base)
	if err := os.RemoveAll(slot); err != nil {
		t.Fatal(err)
	}
	f.commit(f.origin, "remote", "remote\n", "advance origin")
	if _, err := f.sync("ff"); err == nil {
		t.Fatal("missing Wi-owned slot was ignored")
	}
	if got := syncGit(t, f.store, "rev-parse", "main"); got != base {
		t.Fatal("integration advanced despite inconsistent owned slot")
	}
}

func TestRepositorySyncExternalIntegrationReservationStillProtectsItsFiles(t *testing.T) {
	f := newSyncFixture(t)
	base := f.status(false).LocalSHA
	external := filepath.Join(t.TempDir(), "external-main")
	syncGit(t, f.store, "worktree", "add", external, "main")
	if err := os.WriteFile(filepath.Join(external, "document"), []byte("keep external WIP\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.commit(f.origin, "remote", "remote\n", "advance origin")
	if _, err := f.server.repositorySync(context.Background(), RepositorySyncRequest{Selector: "project", Mode: "ff"}); err == nil {
		t.Fatal("checked-out integration ref was advanced")
	}
	if syncGit(t, f.store, "rev-parse", "main") != base {
		t.Fatal("integration changed")
	}
	data, err := os.ReadFile(filepath.Join(external, "document"))
	if err != nil || string(data) != "keep external WIP\n" {
		t.Fatal("external WIP changed", err)
	}
}
