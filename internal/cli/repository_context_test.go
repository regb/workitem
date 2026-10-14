package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSingleRepositoryCommandsDefaultToContext(t *testing.T) {
	for _, fromEnv := range []bool{false, true} {
		t.Run(map[bool]string{false: "checkout", true: "environment"}[fromEnv], func(t *testing.T) {
			cfg, origin, out, _ := remoteCLI(t)
			item, path := newRemoteMergeItem(t, cfg)
			cfg.CWD = filepath.Join(path, "nested")
			if err := os.Mkdir(cfg.CWD, 0700); err != nil {
				t.Fatal(err)
			}
			if fromEnv {
				cfg.CWD = t.TempDir()
				cfg.Env = map[string]string{"WI_ID": item.ID}
			}
			ctx := context.Background()
			for _, args := range [][]string{{"show"}, {"status"}, {"status", "--fetch"}, {"upstream", "--clear"}, {"upstream", origin, "--branch", "main"}, {"rename", "renamed"}, {"archive"}, {"show"}, {"restore"}} {
				out.Reset()
				if err := runRepository(ctx, args, cfg, false); err != nil {
					t.Fatalf("%v: %v", args, err)
				}
			}
			repos, err := cfg.Coordinator.ListRepositories(ctx)
			if err != nil || len(repos) != 1 || repos[0].ID != item.Repository.ID || repos[0].Name != "renamed" || repos[0].Archived {
				t.Fatalf("repos=%+v err=%v", repos, err)
			}
			cfg.Env = map[string]string{"WI_ID": "invalid-context"}
			cfg.CWD = t.TempDir()
			for _, args := range [][]string{{"show", "renamed"}, {"status", "renamed"}, {"upstream", "renamed", "--clear"}, {"upstream", "renamed", origin, "--branch", "main"}, {"rename", "renamed", "explicit"}, {"archive", "explicit"}, {"restore", "explicit"}} {
				if err := runRepository(ctx, args, cfg, false); err != nil {
					t.Fatalf("explicit %v: %v", args, err)
				}
			}
		})
	}
}

func TestRepositoryOperationsOutsideContextRequireSelection(t *testing.T) {
	cfg, origin, _, _ := remoteCLI(t)
	cfg.CWD = t.TempDir()
	for _, args := range [][]string{{"show"}, {"status"}, {"sync"}, {"archive"}, {"restore"}, {"rename", "new-name"}, {"upstream", "--clear"}, {"upstream", origin}} {
		if err := runRepository(context.Background(), args, cfg, false); err == nil || !strings.Contains(err.Error(), "select a repository") {
			t.Fatalf("%v: %v", args, err)
		}
	}
}
