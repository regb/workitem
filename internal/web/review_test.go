package web

import (
	"context"
	"testing"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func (b *fakeBackend) WorkspaceDocument(_ context.Context, _ string, r WorkspaceFileRequest) (*v1.GetWorkspaceDocumentResponse, error) {
	return &v1.GetWorkspaceDocumentResponse{Content: []byte("%PDF-1.7\n" + r.Path), Sha256: "digest", MediaType: "application/pdf"}, nil
}

// A checkout document is served at a URL within its item; the rest of the
// review panel calls the API.
func TestDocumentsStayWithinTheirItem(t *testing.T) {
	s := local(t, &fakeBackend{})
	if w := request(s, "GET", "/api/items/"+itemID+"/document?path=a.pdf&claim_id=claim&claim_generation=1", ""); w.Code != 200 {
		t.Fatalf("document: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "GET", "/api/items/"+missingItem+"/document?path=a.pdf&claim_id=claim&claim_generation=1", ""); w.Code != 404 {
		t.Fatalf("document crossed item boundary: %d", w.Code)
	}
	if w := request(s, "GET", "/api/items/"+itemID+"/review", ""); w.Code != 404 {
		t.Fatalf("review route still served: %d", w.Code)
	}
}
