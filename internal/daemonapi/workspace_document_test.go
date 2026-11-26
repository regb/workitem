package daemonapi_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func TestWorkspaceDocumentRequiresCurrentClaimAndRegularPDF(t *testing.T) {
	f, item, checkout := fileWorkspace(t)
	claim := checkout.Claim
	data := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), 2<<20)...)
	if err := os.WriteFile(filepath.Join(claim.Path, "report.pdf"), data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	req := &v1.GetWorkspaceDocumentRequest{ItemId: item, Path: "report.pdf", ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation}
	result, err := f.Clients.Workspaces.GetWorkspaceDocument(ctx, connect.NewRequest(req))
	if err != nil || !bytes.Equal(result.Msg.Content, data) || result.Msg.MediaType != "application/pdf" || len(result.Msg.Sha256) != 64 {
		t.Fatalf("PDF: %v %v", result, err)
	}
	req.ExpectedClaimGeneration++
	if _, err := f.Clients.Workspaces.GetWorkspaceDocument(ctx, connect.NewRequest(req)); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale claim: %v", err)
	}
	req.ExpectedClaimGeneration = claim.Generation
	req.ExpectedClaimId = ""
	if _, err := f.Clients.Workspaces.GetWorkspaceDocument(ctx, connect.NewRequest(req)); err == nil {
		t.Fatal("missing claim fence accepted")
	}
	req.ExpectedClaimId = claim.Id
	for _, p := range []string{"../report.pdf", ".git/secret.pdf", "/tmp/report.pdf"} {
		req.Path = p
		if _, err := f.Clients.Workspaces.GetWorkspaceDocument(ctx, connect.NewRequest(req)); err == nil {
			t.Fatalf("unsafe path accepted: %s", p)
		}
	}
	req.Path = "missing.pdf"
	if _, err := f.Clients.Workspaces.GetWorkspaceDocument(ctx, connect.NewRequest(req)); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing document: %v", err)
	}
	if err := os.WriteFile(filepath.Join(claim.Path, "fake.pdf"), []byte("<html>not PDF</html>"), 0600); err != nil {
		t.Fatal(err)
	}
	req.Path = "fake.pdf"
	if _, err := f.Clients.Workspaces.GetWorkspaceDocument(ctx, connect.NewRequest(req)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("non-PDF: %v", err)
	}
}
