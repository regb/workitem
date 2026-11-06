package web

import (
	"context"
	"net/http"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// ReviewBackend serves what the review panel needs at a URL rather than as
// an API call: a checkout document for the browser's own viewer. The panel
// calls the rest of the review API directly.
type ReviewBackend interface {
	WorkspaceDocument(context.Context, string, WorkspaceFileRequest) (*v1.GetWorkspaceDocumentResponse, error)
}

// WorkspaceFileRequest names a checkout file, fenced on the claim it was
// read from.
type WorkspaceFileRequest struct {
	Path            string `json:"path"`
	ClaimID         string `json:"claim_id"`
	ClaimGeneration uint32 `json:"claim_generation"`
}

func (s *Server) reviewRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/items/{id}/document", s.item(s.document))
}
