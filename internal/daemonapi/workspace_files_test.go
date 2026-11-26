package daemonapi_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
)

func fileWorkspace(t *testing.T) (*apitest.Fixture, string, *v1.Checkout) {
	t.Helper()
	f := apitest.New(t)
	ctx := context.Background()
	repo := f.Repository(t, "test")
	created, err := f.Clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "Workspace files"}))
	if err != nil {
		t.Fatal(err)
	}
	item := created.Msg.Item.Id
	if _, err = f.Clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: item, State: v1.ItemState_ITEM_STATE_ACTIVE})); err != nil {
		t.Fatal(err)
	}
	workspace, err := f.Clients.Workspaces.EnsureWorkspace(ctx, connect.NewRequest(&v1.EnsureWorkspaceRequest{ItemId: item, SkipSetup: true}))
	if err != nil {
		t.Fatal(err)
	}
	return f, item, workspace.Msg.Checkout
}

func TestWorkspaceSavesSerializeAndRespectBranchOwnership(t *testing.T) {
	f, item, checkout := fileWorkspace(t)
	claim := checkout.Claim
	ctx := context.Background()
	p := filepath.Join(claim.Path, "concurrent.txt")
	if err := os.WriteFile(p, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := f.Clients.Workspaces.GetWorkspaceFile(ctx, connect.NewRequest(&v1.GetWorkspaceFileRequest{ItemId: item, Path: "concurrent.txt", ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation}))
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, text := range []string{"first", "second"} {
		go func(content string) {
			<-start
			_, err := f.Clients.Workspaces.SaveWorkspaceFile(ctx, connect.NewRequest(&v1.SaveWorkspaceFileRequest{ItemId: item, Path: "concurrent.txt", Content: content, ExpectedSha256: source.Msg.File.Sha256, ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation}))
			results <- err
		}(text)
	}
	close(start)
	var successes, conflicts int
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if connect.CodeOf(err) == connect.CodeAborted {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("saves: %d successes, %d conflicts", successes, conflicts)
	}
	written, err := os.ReadFile(p)
	if err != nil || (string(written) != "first" && string(written) != "second") {
		t.Fatalf("file: %q %v", written, err)
	}
	if out, err := exec.Command("git", "-C", claim.Path, "switch", "-c", "unexpected").CombinedOutput(); err != nil {
		t.Fatalf("switch: %v %s", err, out)
	}
	_, err = f.Clients.Workspaces.SaveWorkspaceFile(ctx, connect.NewRequest(&v1.SaveWorkspaceFileRequest{ItemId: item, Path: "concurrent.txt", Content: "not allowed", ExpectedSha256: source.Msg.File.Sha256, ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("branch mismatch: %v", err)
	}
	if out, err := exec.Command("git", "-C", claim.Path, "switch", checkout.Branch).CombinedOutput(); err != nil {
		t.Fatalf("restore: %v %s", err, out)
	}
}

func TestWorkspaceFilesThroughCoreAPI(t *testing.T) {
	f, item, checkout := fileWorkspace(t)
	ctx := context.Background()
	claim := checkout.Claim
	dir := claim.Path
	p := "source with\nnewline.go"
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(p, "old\n")
	write(".gitignore", "ignored\n")
	write("ignored", "secret")
	files, err := f.Clients.Workspaces.ListWorkspaceFiles(ctx, connect.NewRequest(&v1.ListWorkspaceFilesRequest{ItemId: item}))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, path := range files.Msg.Paths {
		if path == p {
			found = true
		}
		if path == "ignored" {
			t.Fatal("ignored path listed")
		}
	}
	if !found {
		t.Fatalf("paths: %v", files.Msg.Paths)
	}
	source, err := f.Clients.Workspaces.GetWorkspaceFile(ctx, connect.NewRequest(&v1.GetWorkspaceFileRequest{ItemId: item, Path: p, ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation}))
	if err != nil {
		t.Fatal(err)
	}
	save := &v1.SaveWorkspaceFileRequest{ItemId: item, Path: p, Content: "edited\n", ExpectedSha256: source.Msg.File.Sha256, ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation}
	saved, err := f.Clients.Workspaces.SaveWorkspaceFile(ctx, connect.NewRequest(save))
	if err != nil || saved.Msg.File.Content != "edited\n" {
		t.Fatalf("save: %v %v", saved, err)
	}
	if _, err = f.Clients.Workspaces.SaveWorkspaceFile(ctx, connect.NewRequest(save)); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale content: %v", err)
	}
	save.ExpectedSha256 = saved.Msg.File.Sha256
	save.ExpectedClaimGeneration++
	if _, err = f.Clients.Workspaces.SaveWorkspaceFile(ctx, connect.NewRequest(save)); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale claim: %v", err)
	}
	save.ExpectedClaimGeneration--
	save.Path = ".git/config"
	if _, err = f.Clients.Workspaces.SaveWorkspaceFile(ctx, connect.NewRequest(save)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("metadata: %v", err)
	}
	if files.Msg.ClaimId != claim.Id || files.Msg.ClaimGeneration != claim.Generation {
		t.Fatalf("listed claim: %v", files.Msg)
	}
	read := &v1.GetWorkspaceFileRequest{ItemId: item, Path: p, ExpectedClaimId: claim.Id}
	if _, err = f.Clients.Workspaces.GetWorkspaceFile(ctx, connect.NewRequest(read)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing claim generation: %v", err)
	}
	read.ExpectedClaimGeneration = claim.Generation
	read.Path = "missing.txt"
	if missing, err := f.Clients.Workspaces.GetWorkspaceFile(ctx, connect.NewRequest(read)); err != nil || missing.Msg.File.Exists {
		t.Fatalf("missing file: %v %v", missing, err)
	}
	save.Path = p
	save.ExpectedSha256 = ""
	if _, err = f.Clients.Workspaces.SaveWorkspaceFile(ctx, connect.NewRequest(save)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing digest: %v", err)
	}
	if _, err = f.Clients.Workspaces.ReleaseWorkspace(ctx, connect.NewRequest(&v1.ReleaseWorkspaceRequest{ItemId: item})); err == nil {
		t.Fatal("released a checkout with uncommitted files")
	}
}
