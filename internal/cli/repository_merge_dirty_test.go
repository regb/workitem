package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/app"
)

func TestMergeOwnedRepositoryPreservesDirtySource(t *testing.T) {
	for _, rebase := range []bool{false, true} {
		t.Run(map[bool]string{false: "fast-forward", true: "isolated-rebase"}[rebase], func(t *testing.T) {
			cfg, origin, out, stderr := remoteCLI(t)
			ctx := context.Background()
			if err := runRepository(ctx, []string{"upstream", "project", origin}, cfg, false); err != nil {
				t.Fatal(err)
			}
			item, path := newRemoteMergeItem(t, cfg)
			source := remoteTestCommit(t, path, "feature.txt", "committed feature\n", "feature")
			if rebase {
				remoteTestCommit(t, origin, "remote.txt", "remote committed\n", "advance upstream")
			}
			remoteTestGit(t, path, "config", "rebase.updateRefs", "true")
			write := func(name, text string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(path, name), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write("feature.txt", "staged feature\n")
			remoteTestGit(t, path, "add", "feature.txt")
			write("feature.txt", "unstaged feature\n")
			write("remote.txt", "local dataset\n")
			index := remoteTestGit(t, path, "ls-files", "--stage")
			staged := remoteTestGit(t, path, "diff", "--cached", "--binary")
			unstaged := remoteTestGit(t, path, "diff", "--binary")
			for iteration := 0; iteration < 2; iteration++ {
				out.Reset()
				if err := runMerge(ctx, []string{"--item", item.ID}, cfg, true); err != nil {
					t.Fatalf("merge failed: %v; stderr=%s", err, stderr.String())
				}
				var result app.MergeResult
				if err := json.Unmarshal(out.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if !result.TargetAdvanced || result.Rebased != rebase || result.SourcePreserved != rebase {
					t.Fatalf("result=%+v", result)
				}
				if got := remoteTestGit(t, path, "rev-parse", "HEAD"); got != source {
					t.Fatal("source branch changed", got)
				}
				if got := remoteTestGit(t, path, "ls-files", "--stage"); got != index {
					t.Fatal("source index changed")
				}
				if remoteTestGit(t, path, "diff", "--cached", "--binary") != staged || remoteTestGit(t, path, "diff", "--binary") != unstaged {
					t.Fatal("staged or unstaged changes were modified")
				}
				data, err := os.ReadFile(filepath.Join(path, "remote.txt"))
				if err != nil || string(data) != "local dataset\n" {
					t.Fatal("source data changed", err)
				}
				if remoteTestGit(t, path, "show", "main:feature.txt") != "committed feature" {
					t.Fatal("merged uncommitted content")
				}
			}
		})
	}
}
