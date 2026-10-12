package agentdriver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/model"
)

type Client struct {
	Driver string
	Path   string
	Args   []string
	Env    []string
}

func (c Client) request(ctx context.Context, operation, agent string, config json.RawMessage, conversation driverprotocol.ConversationRequest) (driverprotocol.ConversationResponse, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}
	request := driverprotocol.Request{ProtocolVersion: driverprotocol.Version, Driver: c.Driver, Operation: operation, Agent: agent, Config: config, Conversation: &conversation}
	env := c.Env
	if env == nil {
		env = os.Environ()
	}
	response, err := RunOperation(ctx, c.Path, c.Args, request, env)
	if err != nil {
		return driverprotocol.ConversationResponse{}, err
	}
	if response.Conversation == nil {
		return driverprotocol.ConversationResponse{}, fmt.Errorf("driver %q omitted its conversation response", c.Driver)
	}
	return *response.Conversation, nil
}

func (c Client) NewConversation(ctx context.Context, id, agent string, config json.RawMessage) (model.Conversation, error) {
	response, err := c.request(ctx, driverprotocol.OperationConversationCreate, agent, config, driverprotocol.ConversationRequest{ID: id})
	if err != nil {
		return model.Conversation{}, err
	}
	return model.Conversation{Driver: c.Driver, ID: response.Conversation.ID, Locator: response.Conversation.Locator, Format: response.Conversation.Format}, nil
}

func (c Client) ConversationCWD(ctx context.Context, path string) (string, error) {
	response, err := c.request(ctx, driverprotocol.OperationConversationCWD, "", nil, driverprotocol.ConversationRequest{Path: path})
	return response.CWD, err
}

func (c Client) ForkConversation(ctx context.Context, sourcePath, targetPath, targetCWD string) error {
	_, err := c.request(ctx, driverprotocol.OperationConversationFork, "", nil, driverprotocol.ConversationRequest{SourcePath: sourcePath, TargetPath: targetPath, TargetCWD: targetCWD})
	return err
}

func (c Client) ObserveConversation(ctx context.Context, conversation model.Conversation, path string, now time.Time) (driverprotocol.ConversationObservation, []string, error) {
	wire := driverprotocol.Conversation{ID: conversation.ID, Locator: conversation.NativeLocator(), Format: conversation.Format}
	response, err := c.request(ctx, driverprotocol.OperationConversationObserve, "", nil, driverprotocol.ConversationRequest{Conversation: wire, Path: path, Now: now})
	if err != nil {
		return driverprotocol.ConversationObservation{}, nil, err
	}
	if response.Observation == nil {
		return driverprotocol.ConversationObservation{}, response.Warnings, fmt.Errorf("driver %q omitted its conversation observation", c.Driver)
	}
	return *response.Observation, response.Warnings, nil
}

func (c Client) DecodeConversation(ctx context.Context, conversation model.Conversation, lineNumbers []uint64, lines [][]byte) ([]driverprotocol.ConversationFact, []bool, error) {
	response, err := c.request(ctx, driverprotocol.OperationConversationDecode, "", nil, driverprotocol.ConversationRequest{Conversation: driverprotocol.Conversation{ID: conversation.ID, Locator: conversation.NativeLocator(), Format: conversation.Format}, LineNumbers: lineNumbers, Lines: lines})
	return response.Facts, response.Decoded, err
}
