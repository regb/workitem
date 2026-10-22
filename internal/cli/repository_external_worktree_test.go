package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/app"
)

func TestMergeIgnoresStaleExternalWorktreeDuringUpstreamSync(t *testing.T) {
	cfg, origin, _, _ := remoteCLI(t)
	ctx := context.Background()
	if err := runRepository(ctx, []string{"upstream", "project", origin}, cfg, false); err != nil {
		t.Fatal(err)
	}
	item, source := newRemoteMergeItem(t, cfg)
	external := filepath.Join(t.TempDir(), "deleted-review")
	remoteTestGit(t, source, "worktree", "add", "--detach", external, "HEAD")
	if err := os.RemoveAll(external); err != nil {
		t.Fatal(err)
	}
	remoteTestCommit(t, source, "feature", "committed feature\n", "feature")
	remoteTestCommit(t, origin, "upstream", "upstream work\n", "advance upstream")
	if err := os.WriteFile(filepath.Join(source, "dataset.csv"), []byte("keep source data\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runMerge(ctx, []string{"--item", item.ID}, cfg, true); err != nil {
		t.Fatal(err)
	}
	if remoteTestGit(t, source, "show", "main:feature") != "committed feature" || remoteTestGit(t, source, "show", "main:upstream") != "upstream work" {
		t.Fatal("committed changes were not integrated")
	}
	if _, err := os.Stat(external); !os.IsNotExist(err) {
		t.Fatal("external checkout was recreated", err)
	}
	if !strings.Contains(remoteTestGit(t, source, "worktree", "list", "--porcelain"), external) {
		t.Fatal("external registration was pruned")
	}
}

func TestMergeOnlySynchronizesWiOwnedTargetCheckouts(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(map[bool]string{false: "external", true: "managed-slot"}[owned], func(t *testing.T) {
			cfg, _, _, _ := remoteCLI(t)
			item, source := newRemoteMergeItem(t, cfg)
			target := filepath.Join(t.TempDir(), "external-review")
			if owned {
				target = filepath.Join(cfg.App.Store.WorktreesDir(), item.Repository.ID, "slot-0050")
			}
			remoteTestGit(t, source, "worktree", "add", "-b", "review", target, "HEAD")
			old := remoteTestGit(t, source, "rev-parse", "review")
			sourceHead := remoteTestCommit(t, source, "feature", "feature\n", "feature")
			if err := os.WriteFile(filepath.Join(target, "private.csv"), []byte("target data\n"), 0600); err != nil {
				t.Fatal(err)
			}
			result, err := cfg.App.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item.ID, Target: "review", Local: true, CWD: source})
			if owned {
				if err != nil || !result.TargetSynced || remoteTestGit(t, source, "rev-parse", "review") != sourceHead {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "external worktree") {
					t.Fatal("external target was adopted", err)
				}
				if remoteTestGit(t, source, "rev-parse", "review") != old {
					t.Fatal("external target ref changed")
				}
				if _, err := os.Stat(filepath.Join(target, "feature")); !os.IsNotExist(err) {
					t.Fatal("external target files updated", err)
				}
			}
			data, readErr := os.ReadFile(filepath.Join(target, "private.csv"))
			if readErr != nil || string(data) != "target data\n" {
				t.Fatal("target data changed", readErr)
			}
		})
	}
}
