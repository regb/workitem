package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/coordinator"
)

func TestRepositorySyncDefaultsToCurrentRepository(t *testing.T) {
	for _, fromEnv := range []bool{false, true} {
		t.Run(map[bool]string{false: "checkout", true: "environment"}[fromEnv], func(t *testing.T) {
			cfg, origin, out, _ := remoteCLI(t)
			item, path := newRemoteMergeItem(t, cfg)
			cfg.CWD = path
			if fromEnv {
				cfg.CWD = t.TempDir()
				cfg.Env = map[string]string{"WI_ID": item.ID}
			} else {
				cfg.CWD = filepath.Join(path, "nested")
				if err := os.Mkdir(cfg.CWD, 0700); err != nil {
					t.Fatal(err)
				}
			}
			next := remoteTestCommit(t, origin, "incoming", "change\n", "incoming")
			out.Reset()
			if err := runRepository(context.Background(), []string{"sync"}, cfg, true); err != nil {
				t.Fatal(err)
			}
			var result coordinator.RepositorySyncResult
			if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Repository.ID != item.Repository.ID || result.LocalSHA != next {
				t.Fatalf("result=%+v err=%v out=%s", result, err, out)
			}
			if remoteTestGit(t, path, "rev-parse", "HEAD") == next {
				t.Fatal("item branch changed")
			}
		})
	}
}

func TestRepositorySyncWithoutContextRequiresName(t *testing.T) {
	cfg, _, _, _ := remoteCLI(t)
	cfg.CWD = t.TempDir()
	if err := runRepository(context.Background(), []string{"sync"}, cfg, false); err == nil || !strings.Contains(err.Error(), "wi repo sync NAME") {
		t.Fatalf("err=%v", err)
	}
	cfg.Env = map[string]string{"WI_ID": "invalid-context"}
	if err := runRepository(context.Background(), []string{"sync", "project"}, cfg, false); err != nil {
		t.Fatal(err)
	}
}
