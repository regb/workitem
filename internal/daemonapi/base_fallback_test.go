package daemonapi

import (
	"connectrpc.com/connect"
	"context"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"path/filepath"
	"testing"
)

func TestCreateFallsBackToLocalBaseWithWarning(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	repository := f.repo(t)
	initial, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repository, Title: "local base"}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.clients.Repositories.SetUpstream(ctx, connect.NewRequest(&v1.SetUpstreamRequest{RepositoryId: repository, Url: filepath.Join(f.root, "missing-upstream.git"), Branch: "main"}))
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repository, Title: "offline fallback"}))
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Msg.Item.CreatedFromCommit != initial.Msg.Item.CreatedFromCommit || len(fallback.Msg.Warnings) == 0 {
		t.Fatal(fallback.Msg)
	}
}
