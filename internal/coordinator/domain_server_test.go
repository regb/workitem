package coordinator

import (
	"context"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

func TestManifestCommandRPCCommits(t *testing.T) {
	root, runtimeDir := t.TempDir(), t.TempDir()
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	manifest := model.NewManifest(importTestID, "rpc-command", "RPC command", nil, false, time.Now().UTC(), model.Repository{}, model.Checkout{})
	if err := native.CreateItem(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	if err := seedRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	socket, _ := SocketPath(runtimeDir, root)
	server, err := NewServer(root, socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	client := &Client{SocketPath: socket}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	canonical, err := client.CanonicalManifest(ctx, importTestID)
	if err != nil {
		t.Fatal(err)
	}
	deep := true
	result, err := client.ExecuteManifestCommand(ctx, ManifestCommand{ID: "rpc-deep", ProtocolVersion: ProtocolVersion, Type: CommandDeepWorkSet, ItemID: importTestID, ExpectedRevision: &canonical.Revision, Actor: "user", DeepWork: &deep, CreatedAt: time.Now().UTC()})
	if err != nil || !result.Changed || !result.Manifest.DeepWork {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := client.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
