package pi

import (
	"context"
	"fmt"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
)

func handleConversationOperation(ctx context.Context, envelope driverprotocol.Request) (driverprotocol.ConversationResponse, error) {
	request := envelope.Conversation
	if request == nil {
		return driverprotocol.ConversationResponse{}, fmt.Errorf("conversation request is required")
	}
	if _, err := parseConfig(envelope.Config); err != nil {
		return driverprotocol.ConversationResponse{}, fmt.Errorf("invalid Pi configuration: %w", err)
	}
	client := New("pi")
	switch envelope.Operation {
	case driverprotocol.OperationConversationCreate:
		conversation := client.NewConversation(request.ID)
		return driverprotocol.ConversationResponse{Conversation: driverprotocol.Conversation{ID: conversation.ID, Locator: conversation.NativeLocator(), Format: conversation.Format}}, nil
	case driverprotocol.OperationConversationCWD:
		cwd, err := client.ConversationCWD(request.Path)
		return driverprotocol.ConversationResponse{CWD: cwd}, err
	case driverprotocol.OperationConversationFork:
		if err := client.ForkConversation(ctx, request.SourcePath, request.TargetPath, request.TargetCWD); err != nil {
			return driverprotocol.ConversationResponse{}, err
		}
		return driverprotocol.ConversationResponse{}, nil
	case driverprotocol.OperationConversationObserve:
		observation, warnings := ObserveConversationPath(request.Path, request.Now)
		return driverprotocol.ConversationResponse{Observation: &observation, Warnings: warnings}, nil
	case driverprotocol.OperationConversationDecode:
		response := driverprotocol.ConversationResponse{Facts: make([]driverprotocol.ConversationFact, 0, len(request.Lines)), Decoded: make([]bool, len(request.Lines))}
		for i, raw := range request.Lines {
			line := request.StartLine + uint64(i)
			if len(request.LineNumbers) == len(request.Lines) {
				line = request.LineNumbers[i]
			}
			fact, ok := SummarizeConversationFact(line, raw)
			response.Decoded[i] = ok
			if ok {
				response.Facts = append(response.Facts, fact)
			}
		}
		return response, nil
	default:
		return driverprotocol.ConversationResponse{}, fmt.Errorf("unsupported Pi driver operation %q", envelope.Operation)
	}
}
