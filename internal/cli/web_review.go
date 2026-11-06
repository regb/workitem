package cli

import (
	"context"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	webaccess "github.com/regb/workitem/internal/web"
)

func (b webBackend) WorkspaceDocument(ctx context.Context, id string, r webaccess.WorkspaceFileRequest) (*v1.GetWorkspaceDocumentResponse, error) {
	value, err := b.cfg.API.Workspaces.GetWorkspaceDocument(ctx, webRequest(ctx, &v1.GetWorkspaceDocumentRequest{ItemId: id, Path: r.Path, ExpectedClaimId: r.ClaimID, ExpectedClaimGeneration: r.ClaimGeneration}))
	if err != nil {
		return nil, err
	}
	return value.Msg, nil
}
