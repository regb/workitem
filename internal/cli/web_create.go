package cli

import (
	"context"
	"strings"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apiview"
	webaccess "github.com/regb/workitem/internal/web"
)

func (b webBackend) Create(ctx context.Context, request webaccess.Create) webaccess.Outcome {
	value, err := b.cfg.API.Lifecycle.CreateAndStartItem(ctx, webRequest(ctx, &v1.CreateAndStartItemRequest{Item: &v1.CreateItemRequest{RepositoryId: request.ProjectID, Description: request.Prompt, GenerateTitle: true, AgentProfile: request.Agent, RequestId: newCommandID()}, Start: &v1.StartOptions{Prompt: request.Prompt, Mode: v1.AgentMode_AGENT_MODE_HEADLESS, ClientMessageId: newCommandID()}}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	m := apiview.Manifest(value.Msg.Item)
	return webaccess.Outcome{Observed: &m, Revision: uint64(value.Msg.Item.Revision)}
}
func (b webBackend) CreateBacklog(ctx context.Context, request webaccess.BacklogCreate) webaccess.Outcome {
	value, err := b.cfg.API.Items.CreateItem(ctx, webRequest(ctx, &v1.CreateItemRequest{RepositoryId: request.ProjectID, Title: request.Title, Description: request.Description, RequestId: newCommandID()}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	m := apiview.Manifest(value.Msg.Item)
	return webaccess.Outcome{Observed: &m, Warnings: value.Msg.Warnings}
}
func (b webBackend) StartCreated(ctx context.Context, id string, revision uint64) webaccess.Outcome {
	value, err := b.cfg.API.Jobs.ListJobs(ctx, webRequest(ctx, &v1.ListJobsRequest{ItemId: id}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	for _, job := range value.Msg.Jobs {
		if job.Kind == "start_item" {
			return b.job(ctx, id, job, "")
		}
	}
	return b.observed(ctx, webaccess.Outcome{}, id)
}
func summaryTitle(prompt string) string {
	line := strings.Join(strings.Fields(prompt), " ")
	if len([]rune(line)) > 72 {
		line = string([]rune(line)[:72])
	}
	if line == "" {
		return "Untitled"
	}
	return line
}
