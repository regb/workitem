package cli

import (
	"context"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apiview"
	webaccess "github.com/regb/workitem/internal/web"
)

func (b webBackend) RepositorySetup(ctx context.Context, project string) (webaccess.RepositorySetup, error) {
	value, err := b.cfg.API.Repositories.GetCheckoutSetup(ctx, webRequest(ctx, &v1.GetCheckoutSetupRequest{RepositoryId: project}))
	if err != nil {
		return webaccess.RepositorySetup{}, err
	}
	v := value.Msg.Setup
	trust := "never"
	if v.Trust == v1.SetupTrust_SETUP_TRUST_ALWAYS {
		trust = "always"
	}
	return webaccess.RepositorySetup{Revision: uint64(value.Msg.Revision), Run: v.Run, Trust: trust, TimeoutSeconds: int64(v.TimeoutSeconds), Skip: apiview.Strings(v.Skip), Declared: v.Declared}, nil
}
func (b webBackend) SetRepositorySetup(ctx context.Context, project string, request webaccess.RepositorySetup) webaccess.Outcome {
	trust := v1.SetupTrust_SETUP_TRUST_NEVER
	if request.Trust == "always" {
		trust = v1.SetupTrust_SETUP_TRUST_ALWAYS
	}
	_, err := b.cfg.API.Repositories.UpdateCheckoutSetup(ctx, webRequest(ctx, &v1.UpdateCheckoutSetupRequest{RepositoryId: project, ExpectedRevision: uint32(request.Revision), Setup: &v1.CheckoutSetup{Run: request.Run, Trust: trust, TimeoutSeconds: uint32(request.TimeoutSeconds), Skip: request.Skip}}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	value, err := b.RepositorySetup(ctx, project)
	return outcome(value, nil, err)
}
func (b webBackend) AddRepositoryFile(ctx context.Context, project string, request webaccess.RepositoryFileRequest) webaccess.Outcome {
	value, err := b.cfg.API.Repositories.PutRepositoryFile(ctx, webRequest(ctx, &v1.PutRepositoryFileRequest{RepositoryId: project, Path: request.Path, Content: request.Content}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return outcome(value.Msg, value.Msg.Warnings, nil)
}
func (b webBackend) RemoveRepositoryFile(ctx context.Context, project string, request webaccess.RepositoryFileRequest) webaccess.Outcome {
	value, err := b.cfg.API.Repositories.DeleteRepositoryFile(ctx, webRequest(ctx, &v1.DeleteRepositoryFileRequest{RepositoryId: project, Path: request.Path}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return outcome(value.Msg, value.Msg.Warnings, nil)
}
