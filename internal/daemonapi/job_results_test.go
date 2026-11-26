package daemonapi

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// A sync's job says what it did: whether the branch moved, and to what.
func TestASyncReportsWhereItLeftTheRepository(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	upstream := filepath.Join(f.root, "upstream")
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "init", "-b", "main", upstream).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	git(upstream, "config", "user.name", "Test")
	git(upstream, "config", "user.email", "test@example.invalid")
	git(upstream, "commit", "--allow-empty", "-m", "initial")
	added, err := f.clients.Repositories.AddRepository(ctx, connect.NewRequest(&v1.AddRepositoryRequest{Name: "synced", Source: upstream, Branch: "main", UpstreamUrl: upstream, UpstreamBranch: "main"}))
	if err != nil {
		t.Fatal(err)
	}
	repo := waitJob(t, f.clients, added.Msg.Job).RepositoryId
	git(upstream, "commit", "--allow-empty", "-m", "upstream moved")
	tip := git(upstream, "rev-parse", "HEAD")
	sync := func() *v1.RepositorySyncResult {
		t.Helper()
		started, err := f.clients.Repositories.SyncRepository(ctx, connect.NewRequest(&v1.SyncRepositoryRequest{RepositoryId: repo, Mode: v1.SyncMode_SYNC_MODE_FAST_FORWARD}))
		if err != nil {
			t.Fatal(err)
		}
		job := waitJob(t, f.clients, started.Msg.Job)
		if job.State != v1.JobState_JOB_STATE_SUCCEEDED || job.GetRepositorySync() == nil {
			t.Fatalf("sync job: %v", job)
		}
		return job.GetRepositorySync()
	}
	if result := sync(); !result.Changed || result.LocalSha != tip || result.UpstreamSha != tip {
		t.Fatalf("first sync: %v, upstream at %s", result, tip)
	}
	if result := sync(); result.Changed || result.LocalSha != tip {
		t.Fatalf("second sync: %v", result)
	}
}
