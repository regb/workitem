package cli

import (
	"context"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apiview"
	webaccess "github.com/regb/workitem/internal/web"
)

func (b webBackend) Create(ctx context.Context, request webaccess.Create) webaccess.Outcome {
	settings := map[string]string{}
	if request.Model != "" {
		settings["model"] = request.Model
	}
	if request.Reasoning != "" {
		settings["reasoning"] = request.Reasoning
	}
	key := requestKey(request.RequestID)
	value, err := b.cfg.API.Lifecycle.CreateAndStartItem(ctx, webRequest(ctx, &v1.CreateAndStartItemRequest{Item: &v1.CreateItemRequest{RepositoryId: request.ProjectID, Description: request.Prompt, GenerateTitle: true, AgentProfile: request.Agent, AgentSettings: settings, RequestId: key}, Start: &v1.StartOptions{Prompt: request.Prompt, Mode: v1.AgentMode_AGENT_MODE_HEADLESS, ClientMessageId: key + "-prompt"}}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	m := apiview.Manifest(value.Msg.Item)
	return webaccess.Outcome{Observed: &m, Revision: uint64(value.Msg.Item.Revision)}
}
func (b webBackend) CreateBacklog(ctx context.Context, request webaccess.BacklogCreate) webaccess.Outcome {
	value, err := b.cfg.API.Items.CreateItem(ctx, webRequest(ctx, &v1.CreateItemRequest{RepositoryId: request.ProjectID, Title: request.Title, Description: request.Description, RequestId: requestKey(request.RequestID)}))
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
			return b.job(ctx, id, job)
		}
	}
	return b.observed(ctx, webaccess.Outcome{}, id)
}

// requestKey is the page's idempotency key, or a new one for a client that
// sends none.
func requestKey(page string) string {
	if page != "" {
		return "web-" + page
	}
	return newCommandID()
}
