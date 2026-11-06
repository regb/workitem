package cli

import (
	"context"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func (b webBackend) TerminalClients(ctx context.Context, id string) (int, error) {
	value, err := b.cfg.API.Agents.GetAgent(ctx, webRequest(ctx, &v1.GetAgentRequest{ItemId: id}))
	if err != nil {
		return 0, err
	}
	if value.Msg.Agent == nil || value.Msg.Agent.Terminal == nil {
		return 0, nil
	}
	return int(value.Msg.Agent.Terminal.AttachedClients), nil
}
