package daemonapi

import (
	"context"
	"errors"
	"os"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/workspacefiles"
)

func sourceFile(f workspacefiles.File) *v1.SourceFile {
	return &v1.SourceFile{Path: f.Path, Content: f.Content, Sha256: f.SHA256, Exists: f.Exists, Binary: f.Binary, TooLarge: f.TooLarge}
}

// expectedClaim requires the claim fence the file methods take.
func expectedClaim(id string, generation uint32) (app.WorkspaceClaim, error) {
	if id == "" || generation == 0 {
		return app.WorkspaceClaim{}, invalid("expected_claim_id and expected_claim_generation are required")
	}
	return app.WorkspaceClaim{ID: id, Generation: generation}, nil
}

// fileError gives the file methods' errors their codes: stale fences abort,
// refused paths and files are invalid, and a checkout that does not allow the
// operation is a failed precondition.
func fileError(err error) error {
	code := connect.Code(0)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, app.ErrWorkspaceClaimChanged), errors.Is(err, workspacefiles.ErrConflict):
		code = connect.CodeAborted
	case errors.Is(err, workspacefiles.ErrInvalidPath), errors.Is(err, workspacefiles.ErrDocument):
		code = connect.CodeInvalidArgument
	case errors.Is(err, app.ErrNoWorkspaceClaim), errors.Is(err, app.ErrNotActive), errors.Is(err, app.ErrCheckoutBranch), errors.Is(err, workspacefiles.ErrNotEditable):
		code = connect.CodeFailedPrecondition
	case errors.Is(err, os.ErrNotExist):
		code = connect.CodeNotFound
	default:
		return rpcError(err)
	}
	return connect.NewError(code, err)
}

func (s *Server) ListWorkspaceFiles(ctx context.Context, req *connect.Request[v1.ListWorkspaceFilesRequest]) (*connect.Response[v1.ListWorkspaceFilesResponse], error) {
	paths, truncated, claim, err := s.App.WorkspaceFilePaths(ctx, resolve(req.Msg.ItemId))
	if err != nil {
		return nil, fileError(err)
	}
	return connect.NewResponse(&v1.ListWorkspaceFilesResponse{Paths: paths, Truncated: truncated, ClaimId: claim.ID, ClaimGeneration: claim.Generation}), nil
}

func (s *Server) GetWorkspaceFile(ctx context.Context, req *connect.Request[v1.GetWorkspaceFileRequest]) (*connect.Response[v1.GetWorkspaceFileResponse], error) {
	r := req.Msg
	claim, err := expectedClaim(r.ExpectedClaimId, r.ExpectedClaimGeneration)
	if err != nil {
		return nil, err
	}
	f, err := s.App.WorkspaceFile(ctx, resolve(r.ItemId), claim, r.Path)
	if err != nil {
		return nil, fileError(err)
	}
	return connect.NewResponse(&v1.GetWorkspaceFileResponse{File: sourceFile(f), ClaimId: claim.ID, ClaimGeneration: claim.Generation}), nil
}

func (s *Server) GetWorkspaceDocument(ctx context.Context, req *connect.Request[v1.GetWorkspaceDocumentRequest]) (*connect.Response[v1.GetWorkspaceDocumentResponse], error) {
	r := req.Msg
	claim, err := expectedClaim(r.ExpectedClaimId, r.ExpectedClaimGeneration)
	if err != nil {
		return nil, err
	}
	data, digest, err := s.App.WorkspaceDocument(ctx, resolve(r.ItemId), claim, r.Path)
	if err != nil {
		return nil, fileError(err)
	}
	return connect.NewResponse(&v1.GetWorkspaceDocumentResponse{Content: data, Sha256: digest, MediaType: "application/pdf"}), nil
}

func (s *Server) SaveWorkspaceFile(ctx context.Context, req *connect.Request[v1.SaveWorkspaceFileRequest]) (*connect.Response[v1.SaveWorkspaceFileResponse], error) {
	r := req.Msg
	claim, err := expectedClaim(r.ExpectedClaimId, r.ExpectedClaimGeneration)
	if err != nil {
		return nil, err
	}
	if r.ExpectedSha256 == "" {
		return nil, invalid("expected_sha256 is required")
	}
	f, err := s.App.SaveWorkspaceFile(ctx, resolve(r.ItemId), claim, r.Path, r.Content, r.ExpectedSha256)
	if err != nil {
		return nil, fileError(err)
	}
	return connect.NewResponse(&v1.SaveWorkspaceFileResponse{File: sourceFile(f)}), nil
}
