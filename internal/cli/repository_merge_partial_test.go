package cli

import (
	"context"
	"strings"
	"testing"
)

func TestMergeRetainsSuccessfulUpstreamSyncWhenItemRebaseFails(t *testing.T) {
	cfg, origin, _, stderr := remoteCLI(t)
	ctx := context.Background()
	if err := runRepository(ctx, []string{"upstream", "project", origin}, cfg, false); err != nil {
		t.Fatal(err)
	}
	manifest, path := newRemoteMergeItem(t, cfg)
	local := remoteTestCommit(t, path, "document", "local change\n", "local")
	remote := remoteTestCommit(t, origin, "document", "remote change\n", "remote")
	err := runMerge(ctx, []string{"--item", manifest.ID}, cfg, false)
	if err == nil || !strings.Contains(err.Error(), "successful upstream synchronization retained") {
		t.Fatalf("err=%v stderr=%s", err, stderr.String())
	}
	if remoteTestGit(t, path, "rev-parse", "HEAD") != local {
		t.Fatal("item not restored")
	}
	if remoteTestGit(t, path, "rev-parse", "main") != remote {
		t.Fatal("successful upstream synchronization rolled back")
	}
}
