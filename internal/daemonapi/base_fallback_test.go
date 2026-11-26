package daemonapi_test

import (
	"context"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
)

func TestCreateFallsBackToLocalBaseWithWarning(t *testing.T) {
	f := apitest.New(t)
	ctx := context.Background()
	repository := f.Repository(t, "test")
	initial, err := f.Clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repository, Title: "local base"}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Clients.Repositories.SetUpstream(ctx, connect.NewRequest(&v1.SetUpstreamRequest{RepositoryId: repository, Url: filepath.Join(f.Root, "missing-upstream.git"), Branch: "main"}))
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := f.Clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repository, Title: "offline fallback"}))
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Msg.Item.CreatedFromCommit != initial.Msg.Item.CreatedFromCommit || len(fallback.Msg.Warnings) == 0 {
		t.Fatal(fallback.Msg)
	}
}
