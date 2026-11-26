package daemonapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/app"
	reviewcore "github.com/regb/workitem/internal/app/core/review"
	"github.com/regb/workitem/internal/coordinator"
	gitpkg "github.com/regb/workitem/internal/git"
)

func codeRevision(r reviewcore.Revision) *v1.CodeRevision {
	out := &v1.CodeRevision{Id: r.ID, CaptureId: r.CaptureID, Commit: r.Commit, Tree: r.Tree, ObservedHead: r.Head, ObservedBranch: r.Branch, DetachedHead: r.Detached, BaseCommit: r.Base, Ref: r.Ref, CreatedAt: stamp(r.CreatedAt), Trigger: revisionTrigger(r.Trigger)}
	for _, e := range r.Excluded {
		out.Excluded = append(out.Excluded, &v1.RevisionExclusion{Path: e.Path, Reason: e.Reason})
	}
	return out
}
func checkpointError(err error) error {
	if errors.Is(err, coordinator.ErrReviewConflict) || errors.Is(err, app.ErrReviewChanged) {
		return aborted(err.Error())
	}
	return rpcError(err)
}
func (s *Server) ListReviewRevisions(ctx context.Context, req *connect.Request[v1.ListReviewRevisionsRequest]) (*connect.Response[v1.ListReviewRevisionsResponse], error) {
	state, err := s.App.ReviewRecordsFor(ctx, resolve(req.Msg.ItemId))
	if err != nil {
		return nil, checkpointError(err)
	}
	start, end, next, err := page(req.Msg.PageToken, req.Msg.PageSize, len(state.Revisions), maxPage)
	if err != nil {
		return nil, err
	}
	out := &v1.ListReviewRevisionsResponse{Version: uint32(state.Version), NextPageToken: next}
	for _, r := range state.Revisions[start:end] {
		out.Revisions = append(out.Revisions, codeRevision(r))
	}
	for _, g := range state.Gaps {
		out.CaptureGaps = append(out.CaptureGaps, &v1.CaptureGap{Id: g.ID, Reason: g.Reason, CreatedAt: stamp(g.CreatedAt)})
	}
	for _, m := range state.Merges {
		out.Merges = append(out.Merges, &v1.ReviewMerge{RevisionId: m.Revision, Commit: m.Commit, Tree: m.Tree, TargetRef: m.Target, Before: m.Before, After: m.After, CreatedAt: stamp(m.CreatedAt)})
	}
	return response(out, nil)
}
func (s *Server) CaptureReviewRevision(ctx context.Context, req *connect.Request[v1.CaptureReviewRevisionRequest]) (*connect.Response[v1.CaptureReviewRevisionResponse], error) {
	r := req.Msg
	opts := app.CheckpointOptions{ResolveOptions: resolve(r.ItemId), CaptureID: r.CaptureId, ClaimID: r.ExpectedClaimId, ClaimGeneration: uint64(r.ExpectedClaimGeneration)}
	out, err := s.App.CaptureCheckpoint(ctx, opts)
	if err != nil {
		return nil, checkpointError(err)
	}
	return response(&v1.CaptureReviewRevisionResponse{Revision: codeRevision(out.Revision), Version: uint32(out.State.Version), Reused: out.Reused}, nil)
}
func (s *Server) revisionEndpoints(ctx context.Context, r *v1.GetRevisionReviewRequest) (string, string, string, string, error) {
	if r == nil || r.ItemId == "" || r.RevisionId == "" {
		return "", "", "", "", invalid("item and revision required")
	}
	state, err := s.App.ReviewRecordsFor(ctx, resolve(r.ItemId))
	if err != nil {
		return "", "", "", "", checkpointError(err)
	}
	target, err := state.Find(r.RevisionId)
	if err != nil {
		return "", "", "", "", checkpointError(err)
	}
	base := target.Base
	if r.FromRevisionId != "" {
		from, err := state.Find(r.FromRevisionId)
		if err != nil {
			return "", "", "", "", checkpointError(err)
		}
		base = from.Commit
	}
	sum := sha256.Sum256([]byte(r.ItemId + "\x00" + base + "\x00" + target.Commit))
	return base, target.Commit, "checkpoint:" + hex.EncodeToString(sum[:]), target.Branch, nil
}
func (s *Server) GetRevisionReview(ctx context.Context, req *connect.Request[v1.GetRevisionReviewRequest]) (*connect.Response[v1.GetRevisionReviewResponse], error) {
	r := req.Msg
	base, head, version, branch, err := s.revisionEndpoints(ctx, r)
	if err != nil {
		return nil, err
	}
	m, err := s.canonical(ctx, r.ItemId, 0)
	if err != nil {
		return nil, rpcError(err)
	}
	g := gitpkg.New("git")
	files, truncated, err := g.ReviewFiles(ctx, m.Manifest.Repository.OperationalRoot(), base, head, false)
	if err != nil {
		return nil, rpcError(err)
	}
	out := &v1.GetReviewResponse{Revision: &v1.ReviewRevision{Version: version, BaseCommit: base, HeadCommit: head}, Branch: branch, IntegrationBranch: m.Manifest.Repository.IntegrationBranch, Truncated: truncated}
	for _, f := range files {
		out.Files = append(out.Files, &v1.ReviewFile{Path: f.Path, Change: fileChange(f.Change)})
	}
	return response(&v1.GetRevisionReviewResponse{Review: out}, nil)
}
func (s *Server) GetRevisionPatch(ctx context.Context, req *connect.Request[v1.GetRevisionPatchRequest]) (*connect.Response[v1.GetRevisionPatchResponse], error) {
	r := req.Msg
	base, head, version, _, err := s.revisionEndpoints(ctx, r.Comparison)
	if err != nil {
		return nil, err
	}
	m, err := s.canonical(ctx, r.Comparison.ItemId, 0)
	if err != nil {
		return nil, rpcError(err)
	}
	patch, err := gitpkg.New("git").ReviewPatch(ctx, m.Manifest.Repository.OperationalRoot(), base, head, r.Path)
	return response(&v1.GetRevisionPatchResponse{Patch: string(patch), Version: version}, err)
}

func (s *Server) GetRevisionReviewFile(ctx context.Context, req *connect.Request[v1.GetRevisionReviewFileRequest]) (*connect.Response[v1.GetRevisionReviewFileResponse], error) {
	r := req.Msg
	base, head, version, _, err := s.revisionEndpoints(ctx, r.Comparison)
	if err != nil {
		return nil, err
	}
	if r.ExpectedVersion == "" || r.ExpectedVersion != version {
		return nil, aborted("checkpoint comparison changed; refresh before reading the file")
	}
	m, err := s.canonical(ctx, r.Comparison.ItemId, 0)
	if err != nil {
		return nil, rpcError(err)
	}
	g := gitpkg.New("git")
	dir := m.Manifest.Repository.OperationalRoot()
	before, err := g.ReviewSource(ctx, dir, base, r.Path)
	if err != nil {
		return nil, rpcError(err)
	}
	after, err := g.ReviewSource(ctx, dir, head, r.Path)
	if err != nil {
		return nil, rpcError(err)
	}
	return response(&v1.GetRevisionReviewFileResponse{File: &v1.GetReviewFileResponse{Revision: &v1.ReviewRevision{Version: version, BaseCommit: base, HeadCommit: head}, OldFile: sourceFile(before), NewFile: sourceFile(after)}}, nil)
}
