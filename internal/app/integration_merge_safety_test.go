package app_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/app"
	gitpkg "github.com/regb/workitem/internal/git"
)

func TestMergeProtectsIgnoredSourceAndDirtyTargetFiles(t *testing.T) {
	t.Run("ignored source", func(t *testing.T) {
		a, repo, source, item := setupMergeWorkItem(t)
		writeMergeFile(t, source, ".gitignore", "ignored.txt\n")
		runMergeGit(t, source, "add", ".gitignore")
		runMergeGit(t, source, "commit", "-m", "ignore local data")
		writeMergeFile(t, source, "ignored.txt", "local data\n")
		writeMergeFile(t, repo, "ignored.txt", "target data\n")
		runMergeGit(t, repo, "add", "ignored.txt")
		runMergeGit(t, repo, "commit", "-m", "start tracking path")
		before := mergeGitOutput(t, repo, "rev-parse", "main")
		if _, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item}); err == nil {
			t.Fatal("ignored source data overwritten")
		}
		if readMergeFile(t, source, "ignored.txt") != "local data\n" || mergeGitOutput(t, repo, "rev-parse", "main") != before {
			t.Fatal("unsafe rebase changed data")
		}
	})
	for _, overlap := range []bool{false, true} {
		name := "unrelated"
		if overlap {
			name = "overlap"
		}
		t.Run(name, func(t *testing.T) {
			a, repo, source, item := setupMergeWorkItem(t)
			writeMergeFile(t, source, "README.md", "source work\n")
			path := "local-note"
			if overlap {
				path = "README.md"
			}
			writeMergeFile(t, repo, path, "keep target work\n")
			before := mergeGitOutput(t, repo, "rev-parse", "main")
			_, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item})
			if overlap {
				if err == nil || mergeGitOutput(t, repo, "rev-parse", "main") != before {
					t.Fatal("overlapping dirty target changed")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if readMergeFile(t, repo, path) != "keep target work\n" {
				t.Fatal("target local work lost")
			}
		})
	}
}
func TestMergeRebaseNeverUpdatesOtherRefs(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	writeMergeFile(t, source, "feature.txt", "feature")
	runMergeGit(t, source, "add", ".")
	runMergeGit(t, source, "commit", "-m", "feature")
	old := mergeGitOutput(t, source, "rev-parse", "HEAD")
	runMergeGit(t, source, "branch", "retained-ref", old)
	runMergeGit(t, source, "config", "rebase.updateRefs", "true")
	writeMergeFile(t, repo, "target.txt", "target")
	runMergeGit(t, repo, "add", ".")
	runMergeGit(t, repo, "commit", "-m", "target")
	if _, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item, Rebase: true}); err != nil {
		t.Fatal(err)
	}
	if mergeGitOutput(t, repo, "rev-parse", "retained-ref") != old {
		t.Fatal("rebase.updateRefs rewrote another branch")
	}
}
func TestMergeExclusionsRemainInformational(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	file, err := os.Create(filepath.Join(source, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(gitpkg.SnapshotFileLimit + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item})
	if err != nil {
		t.Fatal("exclusion blocked merge", err)
	}
	state, _ := a.ReviewRecords.LoadReview(context.Background(), item)
	if len(state.Revisions[0].Excluded) == 0 || mergeGitOutput(t, repo, "rev-parse", result.Commit+":large.bin") == "" {
		t.Fatal("excluded path was not integrated")
	}
}
