package daemonapi

import (
	"context"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/app"
	gitpkg "github.com/regb/workitem/internal/git"
)

func reviewOptions(comparison v1.ReviewComparison, commit string) (app.ReviewOptions, error) {
	names := map[v1.ReviewComparison]string{v1.ReviewComparison_REVIEW_COMPARISON_UNSPECIFIED: "item", v1.ReviewComparison_REVIEW_COMPARISON_ITEM: "item", v1.ReviewComparison_REVIEW_COMPARISON_COMMITTED: "committed", v1.ReviewComparison_REVIEW_COMPARISON_UNCOMMITTED: "uncommitted", v1.ReviewComparison_REVIEW_COMPARISON_COMMIT: "commit", v1.ReviewComparison_REVIEW_COMPARISON_INTEGRATION: "integration"}
	name, ok := names[comparison]
	if !ok {
		return app.ReviewOptions{}, invalid("unknown review comparison")
	}
	return app.ReviewOptions{Comparison: name, Commit: commit}, nil
}
func reviewRevision(r app.ReviewRevision) *v1.ReviewRevision {
	return &v1.ReviewRevision{Version: r.Version, BaseCommit: r.BaseCommit, HeadCommit: r.HeadCommit, ClaimId: r.ClaimID, ClaimGeneration: r.ClaimGeneration, WorkingTree: r.WorkingTree}
}
func (s *Server) GetReview(ctx context.Context, req *connect.Request[v1.GetReviewRequest]) (*connect.Response[v1.GetReviewResponse], error) {
	opts, err := reviewOptions(req.Msg.Comparison, req.Msg.Commit)
	if err != nil {
		return nil, err
	}
	opts.BaseCommit = req.Msg.BaseCommit
	value, err := s.App.GetItemReview(ctx, resolve(req.Msg.ItemId), opts)
	if err != nil {
		return nil, rpcError(err)
	}
	out := &v1.GetReviewResponse{Revision: reviewRevision(value.Revision), Truncated: value.Truncated, Branch: value.Branch, IntegrationBranch: value.IntegrationBranch}
	for _, f := range value.Files {
		out.Files = append(out.Files, &v1.ReviewFile{Path: f.Path, Change: f.Change})
	}
	return response(out, nil)
}
func (s *Server) GetReviewFile(ctx context.Context, req *connect.Request[v1.GetReviewFileRequest]) (*connect.Response[v1.GetReviewFileResponse], error) {
	opts, err := reviewOptions(req.Msg.Comparison, req.Msg.Commit)
	if err != nil {
		return nil, err
	}
	if req.Msg.ExpectedVersion == "" {
		return nil, invalid("expected_version is required")
	}
	opts.BaseCommit = req.Msg.BaseCommit
	r, old, next, err := s.App.GetItemReviewFile(ctx, resolve(req.Msg.ItemId), opts, req.Msg.Path, req.Msg.ExpectedVersion)
	return response(&v1.GetReviewFileResponse{Revision: reviewRevision(r), OldFile: sourceFile(old), NewFile: sourceFile(next)}, err)
}
func (s *Server) ListReviewCommits(ctx context.Context, req *connect.Request[v1.ListReviewCommitsRequest]) (*connect.Response[v1.ListReviewCommitsResponse], error) {
	out := &v1.ListReviewCommitsResponse{}
	var commits []gitpkg.ReviewCommit
	if req.Msg.IncludeContext {
		graph, err := s.App.ItemReviewGraph(ctx, resolve(req.Msg.ItemId))
		if err != nil {
			return nil, rpcError(err)
		}
		commits, out.Truncated, out.Warnings = graph.Commits, graph.Truncated, graph.Warnings
		for _, branch := range graph.Branches {
			out.Branches = append(out.Branches, &v1.ReviewBranch{Name: branch.Name, Sha: branch.SHA, Kind: branch.Kind})
		}
	} else {
		var err error
		commits, out.Truncated, err = s.App.ItemReviewCommits(ctx, resolve(req.Msg.ItemId))
		if err != nil {
			return nil, rpcError(err)
		}
	}
	for _, c := range commits {
		out.Commits = append(out.Commits, &v1.ReviewCommit{Sha: c.SHA, Subject: c.Subject, Author: c.Author, AuthoredAt: c.AuthoredAt, ParentShas: c.Parents})
	}
	return response(out, nil)
}
