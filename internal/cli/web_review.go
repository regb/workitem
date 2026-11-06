package cli

import (
	"context"
	"fmt"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	webaccess "github.com/regb/workitem/internal/web"
)

func webComparison(name string) (v1.ReviewComparison, error) {
	switch name {
	case "", "item":
		return v1.ReviewComparison_REVIEW_COMPARISON_ITEM, nil
	case "committed":
		return v1.ReviewComparison_REVIEW_COMPARISON_COMMITTED, nil
	case "uncommitted":
		return v1.ReviewComparison_REVIEW_COMPARISON_UNCOMMITTED, nil
	case "integration":
		return v1.ReviewComparison_REVIEW_COMPARISON_INTEGRATION, nil
	case "commit":
		return v1.ReviewComparison_REVIEW_COMPARISON_COMMIT, nil
	default:
		return 0, fmt.Errorf("invalid review comparison")
	}
}
func checkpointComparison(id string, r webaccess.ReviewRequest) *v1.GetRevisionReviewRequest {
	return &v1.GetRevisionReviewRequest{ItemId: id, RevisionId: r.RevisionID, FromRevisionId: r.FromRevisionID}
}
func (b webBackend) ReviewRevisions(ctx context.Context, id string) (*v1.ListReviewRevisionsResponse, error) {
	value, err := b.cfg.API.Reviews.ListReviewRevisions(ctx, webRequest(ctx, &v1.ListReviewRevisionsRequest{ItemId: id}))
	if err != nil {
		return nil, err
	}
	return value.Msg, nil
}
func (b webBackend) CaptureRevision(ctx context.Context, id string, r webaccess.CaptureRevisionRequest) webaccess.Outcome {
	value, err := b.cfg.API.Reviews.CaptureReviewRevision(ctx, webRequest(ctx, &v1.CaptureReviewRevisionRequest{ItemId: id, CaptureId: r.CaptureID, ExpectedClaimId: r.ClaimID, ExpectedClaimGeneration: uint32(r.ClaimGeneration)}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return outcome(value.Msg, nil, nil)
}
func (b webBackend) Review(ctx context.Context, id string, r webaccess.ReviewRequest) (*v1.GetReviewResponse, error) {
	if r.RevisionID != "" {
		value, err := b.cfg.API.Reviews.GetRevisionReview(ctx, webRequest(ctx, checkpointComparison(id, r)))
		if err != nil {
			return nil, err
		}
		return value.Msg.Review, nil
	}
	comparison, err := webComparison(r.Comparison)
	if err != nil {
		return nil, err
	}
	value, err := b.cfg.API.Reviews.GetReview(ctx, webRequest(ctx, &v1.GetReviewRequest{ItemId: id, Comparison: comparison, Commit: r.Commit, BaseCommit: r.BaseCommit}))
	if err != nil {
		return nil, err
	}
	return value.Msg, nil
}
func (b webBackend) ReviewFile(ctx context.Context, id string, r webaccess.ReviewFileRequest) (*v1.GetReviewFileResponse, error) {
	if r.RevisionID != "" {
		value, err := b.cfg.API.Reviews.GetRevisionReviewFile(ctx, webRequest(ctx, &v1.GetRevisionReviewFileRequest{Comparison: checkpointComparison(id, r.ReviewRequest), Path: r.Path, ExpectedVersion: r.ExpectedVersion}))
		if err != nil {
			return nil, err
		}
		return value.Msg.File, nil
	}
	comparison, err := webComparison(r.Comparison)
	if err != nil {
		return nil, err
	}
	value, err := b.cfg.API.Reviews.GetReviewFile(ctx, webRequest(ctx, &v1.GetReviewFileRequest{ItemId: id, Comparison: comparison, Commit: r.Commit, BaseCommit: r.BaseCommit, Path: r.Path, ExpectedVersion: r.ExpectedVersion}))
	if err != nil {
		return nil, err
	}
	return value.Msg, nil
}
func (b webBackend) ReviewCommits(ctx context.Context, id string) (*v1.ListReviewCommitsResponse, error) {
	value, err := b.cfg.API.Reviews.ListReviewCommits(ctx, webRequest(ctx, &v1.ListReviewCommitsRequest{ItemId: id, IncludeContext: true}))
	if err != nil {
		return nil, err
	}
	return value.Msg, nil
}
func (b webBackend) WorkspaceFiles(ctx context.Context, id string) (*v1.ListWorkspaceFilesResponse, error) {
	value, err := b.cfg.API.Workspaces.ListWorkspaceFiles(ctx, webRequest(ctx, &v1.ListWorkspaceFilesRequest{ItemId: id}))
	if err != nil {
		return nil, err
	}
	return value.Msg, nil
}
func (b webBackend) WorkspaceFile(ctx context.Context, id string, r webaccess.WorkspaceFileRequest) (*v1.GetWorkspaceFileResponse, error) {
	value, err := b.cfg.API.Workspaces.GetWorkspaceFile(ctx, webRequest(ctx, &v1.GetWorkspaceFileRequest{ItemId: id, Path: r.Path, ExpectedClaimId: r.ClaimID, ExpectedClaimGeneration: r.ClaimGeneration}))
	if err != nil {
		return nil, err
	}
	return value.Msg, nil
}
func (b webBackend) WorkspaceDocument(ctx context.Context, id string, r webaccess.WorkspaceFileRequest) (*v1.GetWorkspaceDocumentResponse, error) {
	value, err := b.cfg.API.Workspaces.GetWorkspaceDocument(ctx, webRequest(ctx, &v1.GetWorkspaceDocumentRequest{ItemId: id, Path: r.Path, ExpectedClaimId: r.ClaimID, ExpectedClaimGeneration: r.ClaimGeneration}))
	if err != nil {
		return nil, err
	}
	return value.Msg, nil
}

func (b webBackend) SaveFile(ctx context.Context, id string, r webaccess.SaveFileRequest) webaccess.Outcome {
	value, err := b.cfg.API.Workspaces.SaveWorkspaceFile(ctx, webRequest(ctx, &v1.SaveWorkspaceFileRequest{ItemId: id, Path: r.Path, Content: r.Content, ExpectedSha256: r.ExpectedSHA256, ExpectedClaimId: r.ClaimID, ExpectedClaimGeneration: r.ClaimGeneration}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return outcome(value.Msg, nil, nil)
}
func (b webBackend) CommitChanges(ctx context.Context, id string, r webaccess.CommitRequest) webaccess.Outcome {
	value, err := b.cfg.API.Workspaces.CommitWorkspace(ctx, webRequest(ctx, &v1.CommitWorkspaceRequest{ItemId: id, Message: r.Message}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return outcome(value.Msg, nil, nil)
}
func (b webBackend) Merge(ctx context.Context, id string, r webaccess.MergeRequest) webaccess.Outcome {
	value, err := b.cfg.API.Repositories.IntegrateItem(ctx, webRequest(ctx, &v1.IntegrateItemRequest{ItemId: id, Message: r.Message, Rebase: r.Rebase}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return b.job(ctx, id, value.Msg.Job)
}
