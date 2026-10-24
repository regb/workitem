package agentdriver

import (
	"context"
	"fmt"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
)

func (c Client) History(ctx context.Context, path, leaf string) (driverprotocol.History, error) {
	return c.HistoryPage(ctx, path, leaf, "")
}
func (c Client) HistoryPage(ctx context.Context, path, leaf, before string) (driverprotocol.History, error) {
	res, err := c.request(ctx, driverprotocol.OperationConversationHistory, "", nil, driverprotocol.ConversationRequest{Path: path, LeafID: leaf, BeforeID: before})
	if err != nil {
		return driverprotocol.History{}, err
	}
	if res.History == nil {
		return driverprotocol.History{}, fmt.Errorf("driver does not provide readable history")
	}
	return *res.History, nil
}

// SubagentHistory reads a subagent's conversation, which only a driver with
// the subagents capability can tell apart from the main one.
func (c Client) SubagentHistory(ctx context.Context, path, subagent, before string) (driverprotocol.History, error) {
	capabilities, err := c.ConversationCapabilities(ctx)
	if err != nil {
		return driverprotocol.History{}, err
	}
	if !capabilities.Subagents {
		return driverprotocol.History{}, fmt.Errorf("driver does not provide subagent history")
	}
	res, err := c.request(ctx, driverprotocol.OperationConversationHistory, "", nil, driverprotocol.ConversationRequest{Path: path, Subagent: subagent, BeforeID: before})
	if err != nil {
		return driverprotocol.History{}, err
	}
	if res.History == nil {
		return driverprotocol.History{}, fmt.Errorf("driver does not provide subagent history")
	}
	return *res.History, nil
}
