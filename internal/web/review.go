package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// ReviewBackend is an item-scoped client of the core API. No filesystem or Git
// access belongs in the browser adapter.
type ReviewBackend interface {
	ReviewIntent(context.Context, string, string) (*v1.GetReviewIntentResponse, error)
	SetReviewIntent(context.Context, string, *v1.SetReviewIntentRequest) Outcome
	NotifyReviewRound(context.Context, string, *v1.NotifyReviewRoundRequest) Outcome
	Review(context.Context, string, ReviewRequest) (*v1.GetReviewResponse, error)
	ReviewRevisions(context.Context, string) (*v1.ListReviewRevisionsResponse, error)
	CaptureRevision(context.Context, string, CaptureRevisionRequest) Outcome
	ReviewThreads(context.Context, string) (*v1.GetReviewThreadsResponse, error)
	ReviewDrafts(context.Context, string) (*v1.GetReviewDraftsResponse, error)
	SaveReviewDraft(context.Context, string, *v1.SaveReviewDraftRequest) Outcome
	DeleteReviewDrafts(context.Context, string, *v1.DeleteReviewDraftsRequest) Outcome
	PublishReviewRound(context.Context, string, *v1.PublishReviewRoundRequest) Outcome
	ReplyReviewThreads(context.Context, string, *v1.ReplyReviewThreadsRequest) Outcome
	ReviewFile(context.Context, string, ReviewFileRequest) (*v1.GetReviewFileResponse, error)
	ReviewCommits(context.Context, string) (*v1.ListReviewCommitsResponse, error)
	WorkspaceFiles(context.Context, string) (*v1.ListWorkspaceFilesResponse, error)
	WorkspaceFile(context.Context, string, WorkspaceFileRequest) (*v1.GetWorkspaceFileResponse, error)
	WorkspaceDocument(context.Context, string, WorkspaceFileRequest) (*v1.GetWorkspaceDocumentResponse, error)
	SaveFile(context.Context, string, SaveFileRequest) Outcome
	CommitChanges(context.Context, string, CommitRequest) Outcome
	Merge(context.Context, string, MergeRequest) Outcome
}

type MergeRequest struct {
	Message string `json:"message"`
	Rebase  bool   `json:"rebase"`
}

type ReviewRequest struct {
	Comparison     string `json:"comparison"`
	Commit         string `json:"commit"`
	BaseCommit     string `json:"base_commit"`
	RevisionID     string `json:"revision_id"`
	FromRevisionID string `json:"from_revision_id"`
}
type ReviewFileRequest struct {
	ReviewRequest
	Path            string `json:"path"`
	ExpectedVersion string `json:"expected_version"`
}
type CaptureRevisionRequest struct {
	CaptureID       string `json:"capture_id"`
	ClaimID         string `json:"claim_id"`
	ClaimGeneration uint64 `json:"claim_generation"`
}

type WorkspaceFileRequest struct {
	Path            string `json:"path"`
	ClaimID         string `json:"claim_id"`
	ClaimGeneration uint32 `json:"claim_generation"`
}
type SaveFileRequest struct {
	WorkspaceFileRequest
	Content        string `json:"content"`
	ExpectedSHA256 string `json:"expected_sha256"`
}
type CommitRequest struct {
	Message string `json:"message"`
}

func reviewRequest(r *http.Request) ReviewRequest {
	q := r.URL.Query()
	return ReviewRequest{Comparison: q.Get("comparison"), Commit: q.Get("commit"), BaseCommit: q.Get("base_commit"), RevisionID: q.Get("revision_id"), FromRevisionID: q.Get("from_revision_id")}
}

func requiredReviewRequest[T any](r **T) error {
	if *r == nil {
		return errors.New("request object is required")
	}
	return nil
}
func (s *Server) reviewRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/items/{id}/review-intent", s.item(func(w http.ResponseWriter, r *http.Request, id string) {
		v, err := s.backend.ReviewIntent(r.Context(), id, r.URL.Query().Get("revision_id"))
		reply(w, v, err)
	}))
	m.HandleFunc("POST /api/items/{id}/review-intent", s.item(mutate(64<<10, requiredReviewRequest[v1.SetReviewIntentRequest], s.backend.SetReviewIntent)))
	m.HandleFunc("POST /api/items/{id}/review-notify", s.item(mutate(8<<10, requiredReviewRequest[v1.NotifyReviewRoundRequest], s.backend.NotifyReviewRound)))
	m.HandleFunc("GET /api/items/{id}/review-threads", s.item(func(w http.ResponseWriter, r *http.Request, id string) {
		value, err := s.backend.ReviewThreads(r.Context(), id)
		reply(w, value, err)
	}))
	m.HandleFunc("GET /api/items/{id}/review-drafts", s.item(func(w http.ResponseWriter, r *http.Request, id string) {
		value, err := s.backend.ReviewDrafts(r.Context(), id)
		reply(w, value, err)
	}))
	m.HandleFunc("POST /api/items/{id}/review-draft", s.item(mutate(64<<10, requiredReviewRequest[v1.SaveReviewDraftRequest], s.backend.SaveReviewDraft)))
	m.HandleFunc("POST /api/items/{id}/review-drafts/delete", s.item(mutate(128<<10, requiredReviewRequest[v1.DeleteReviewDraftsRequest], s.backend.DeleteReviewDrafts)))
	m.HandleFunc("POST /api/items/{id}/review-round", s.item(mutate(128<<10, requiredReviewRequest[v1.PublishReviewRoundRequest], s.backend.PublishReviewRound)))
	m.HandleFunc("POST /api/items/{id}/review-replies", s.item(mutate(8<<20, requiredReviewRequest[v1.ReplyReviewThreadsRequest], s.backend.ReplyReviewThreads)))
	m.HandleFunc("GET /api/items/{id}/review-revisions", s.item(func(w http.ResponseWriter, r *http.Request, id string) {
		value, err := s.backend.ReviewRevisions(r.Context(), id)
		reply(w, value, err)
	}))
	m.HandleFunc("POST /api/items/{id}/checkpoints", s.item(mutate(8<<10, nil, s.backend.CaptureRevision)))
	m.HandleFunc("GET /api/items/{id}/review", s.item(func(w http.ResponseWriter, r *http.Request, id string) {
		v, err := s.backend.Review(r.Context(), id, reviewRequest(r))
		reply(w, v, err)
	}))
	m.HandleFunc("GET /api/items/{id}/review-file", s.item(func(w http.ResponseWriter, r *http.Request, id string) {
		q := r.URL.Query()
		v, err := s.backend.ReviewFile(r.Context(), id, ReviewFileRequest{reviewRequest(r), q.Get("path"), q.Get("expected_version")})
		reply(w, v, err)
	}))
	m.HandleFunc("GET /api/items/{id}/commits", s.item(func(w http.ResponseWriter, r *http.Request, id string) {
		v, err := s.backend.ReviewCommits(r.Context(), id)
		reply(w, v, err)
	}))
	m.HandleFunc("GET /api/items/{id}/workspace-files", s.item(func(w http.ResponseWriter, r *http.Request, id string) {
		v, err := s.backend.WorkspaceFiles(r.Context(), id)
		reply(w, v, err)
	}))
	m.HandleFunc("GET /api/items/{id}/source", s.item(func(w http.ResponseWriter, r *http.Request, id string) {
		q := r.URL.Query()
		generation, err := strconv.ParseUint(q.Get("claim_generation"), 10, 32)
		if err != nil {
			badRequest(w, err)
			return
		}
		v, err := s.backend.WorkspaceFile(r.Context(), id, WorkspaceFileRequest{q.Get("path"), q.Get("claim_id"), uint32(generation)})
		reply(w, v, err)
	}))
	m.HandleFunc("GET /api/items/{id}/document", s.item(s.document))
	m.HandleFunc("POST /api/items/{id}/source", s.item(mutate(7<<20, nil, s.backend.SaveFile)))
	m.HandleFunc("POST /api/items/{id}/commit", s.item(mutate(8<<10, nil, s.backend.CommitChanges)))
	m.HandleFunc("POST /api/items/{id}/merge", s.item(mutate(32<<10, nil, s.backend.Merge)))
}
