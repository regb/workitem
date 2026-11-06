package web

import (
	"context"
	"net/url"
	"strings"
	"testing"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func (b *fakeBackend) ReviewIntent(context.Context, string, string) (*v1.GetReviewIntentResponse, error) {
	return &v1.GetReviewIntentResponse{}, nil
}
func (b *fakeBackend) SetReviewIntent(context.Context, string, *v1.SetReviewIntentRequest) Outcome {
	return Outcome{}
}
func (b *fakeBackend) NotifyReviewRound(context.Context, string, *v1.NotifyReviewRoundRequest) Outcome {
	return Outcome{}
}
func (b *fakeBackend) Review(_ context.Context, _ string, r ReviewRequest) (*v1.GetReviewResponse, error) {
	return &v1.GetReviewResponse{Branch: "wi/test", Revision: &v1.ReviewRevision{BaseCommit: r.BaseCommit, HeadCommit: r.RevisionID}}, nil
}
func (b *fakeBackend) ReviewRevisions(context.Context, string) (*v1.ListReviewRevisionsResponse, error) {
	return &v1.ListReviewRevisionsResponse{}, nil
}
func (b *fakeBackend) CaptureRevision(_ context.Context, _ string, r CaptureRevisionRequest) Outcome {
	return Outcome{Result: &v1.CaptureReviewRevisionResponse{Revision: &v1.CodeRevision{Id: "r1", CaptureId: r.CaptureID}}}
}
func (b *fakeBackend) ReviewThreads(context.Context, string) (*v1.GetReviewThreadsResponse, error) {
	return &v1.GetReviewThreadsResponse{}, nil
}
func (b *fakeBackend) ReviewDrafts(context.Context, string) (*v1.GetReviewDraftsResponse, error) {
	return &v1.GetReviewDraftsResponse{}, nil
}
func (b *fakeBackend) SaveReviewDraft(_ context.Context, _ string, r *v1.SaveReviewDraftRequest) Outcome {
	return Outcome{Result: &v1.ReviewMutation{DraftId: "d1"}}
}
func (b *fakeBackend) DeleteReviewDrafts(context.Context, string, *v1.DeleteReviewDraftsRequest) Outcome {
	return Outcome{}
}
func (b *fakeBackend) PublishReviewRound(_ context.Context, _ string, r *v1.PublishReviewRoundRequest) Outcome {
	return Outcome{Result: &v1.ReviewMutation{RoundId: "q1"}}
}
func (b *fakeBackend) ReplyReviewThreads(context.Context, string, *v1.ReplyReviewThreadsRequest) Outcome {
	return Outcome{}
}
func (b *fakeBackend) ReviewFile(_ context.Context, _ string, r ReviewFileRequest) (*v1.GetReviewFileResponse, error) {
	return &v1.GetReviewFileResponse{Revision: &v1.ReviewRevision{BaseCommit: r.BaseCommit, HeadCommit: r.RevisionID}}, nil
}
func (b *fakeBackend) ReviewCommits(context.Context, string) (*v1.ListReviewCommitsResponse, error) {
	return &v1.ListReviewCommitsResponse{}, nil
}
func (b *fakeBackend) WorkspaceFiles(context.Context, string) (*v1.ListWorkspaceFilesResponse, error) {
	return &v1.ListWorkspaceFilesResponse{Paths: []string{"a b.txt"}, ClaimId: "claim", ClaimGeneration: 1}, nil
}
func (b *fakeBackend) WorkspaceFile(_ context.Context, _ string, r WorkspaceFileRequest) (*v1.GetWorkspaceFileResponse, error) {
	return &v1.GetWorkspaceFileResponse{File: &v1.SourceFile{Path: r.Path, Exists: true, Content: "source"}}, nil
}
func (b *fakeBackend) WorkspaceDocument(_ context.Context, _ string, r WorkspaceFileRequest) (*v1.GetWorkspaceDocumentResponse, error) {
	return &v1.GetWorkspaceDocumentResponse{Content: []byte("%PDF-1.7\n" + r.Path), Sha256: "digest", MediaType: "application/pdf"}, nil
}
func (b *fakeBackend) SaveFile(_ context.Context, _ string, r SaveFileRequest) Outcome {
	return Outcome{Result: &v1.SaveWorkspaceFileResponse{File: &v1.SourceFile{Path: r.Path, Content: r.Content}}}
}
func (b *fakeBackend) CommitChanges(context.Context, string, CommitRequest) Outcome { return Outcome{} }
func (b *fakeBackend) Merge(context.Context, string, MergeRequest) Outcome          { return Outcome{} }

func TestReviewRoutesUseItemBoundary(t *testing.T) {
	s := local(t, &fakeBackend{})
	for _, action := range []string{"review", "review-file?comparison=item&path=a&expected_version=v", "commits", "workspace-files", "review-revisions", "review-threads", "review-drafts", "source?path=" + url.QueryEscape("a b.txt") + "&claim_id=claim&claim_generation=1"} {
		w := request(s, "GET", "/api/items/"+itemID+"/"+action, "")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", action, w.Code, w.Body.String())
		}
		w = request(s, "GET", "/api/items/"+missingItem+"/"+action, "")
		if w.Code != 404 {
			t.Fatalf("missing item: %d", w.Code)
		}
	}
	for _, action := range []string{"review?base_commit=selected-base", "review-file?path=a&expected_version=v&base_commit=selected-base"} {
		w := request(s, "GET", "/api/items/"+itemID+"/"+action, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"base_commit":"selected-base"`) {
			t.Fatalf("base not forwarded: %d %s", w.Code, w.Body.String())
		}
	}
	for _, action := range []string{"review?revision_id=r2&from_revision_id=r1", "review-file?revision_id=r2&path=a&expected_version=v"} {
		w := request(s, "GET", "/api/items/"+itemID+"/"+action, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"head_commit":"r2"`) {
			t.Fatalf("checkpoint selection not forwarded: %d %s", w.Code, w.Body.String())
		}
	}
	w := request(s, "POST", "/api/items/"+itemID+"/checkpoints", `{"capture_id":"capture-1","claim_id":"claim","claim_generation":1}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"capture_id":"capture-1"`) {
		t.Fatalf("capture: %d %s", w.Code, w.Body.String())
	}
	w = request(s, "POST", "/api/items/"+missingItem+"/checkpoints", `{"capture_id":"capture-1","claim_id":"claim","claim_generation":1}`)
	if w.Code != 404 {
		t.Fatalf("capture crossed item boundary: %d %s", w.Code, w.Body.String())
	}
	w = request(s, "POST", "/api/items/"+itemID+"/source", `{"path":"a b.txt","content":"new","expected_sha256":"old","claim_id":"claim","claim_generation":1}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"content":"new"`) {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	for _, action := range []string{"review-draft", "review-drafts/delete", "review-round", "review-replies"} {
		w = request(s, "POST", "/api/items/"+itemID+"/"+action, "null")
		if w.Code != 400 {
			t.Fatalf("null %s request: %d %s", action, w.Code, w.Body.String())
		}
		w = request(s, "POST", "/api/items/"+missingItem+"/"+action, `{"revision_id":"r1"}`)
		if w.Code != 404 {
			t.Fatalf("review mutation crossed item boundary: %d", w.Code)
		}
	}
	w = request(s, "GET", "/api/items/"+itemID+"/source?path=x&claim_generation=bogus", "")
	if w.Code != 400 {
		t.Fatalf("generation: %d", w.Code)
	}
}
