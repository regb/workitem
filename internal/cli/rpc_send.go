package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// rpcSend says something to an item's agent, as a person or another agent
// would, starting the agent when the item has none running. With --wait it
// returns the agent's answer: its last reply once the turn the message
// started has settled. An agent asking another item's agent gets its answer
// as the command's result, without writing back to it.
func rpcSend(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("send", cfg.Stderr)
	wait := fs.Bool("wait", false, "wait for the agent's answer")
	timeout := fs.Duration("timeout", 30*time.Minute, "how long --wait waits")
	followUp := fs.Bool("follow-up", false, "deliver after the current turn settles")
	file := fs.String("file", "", "read the message from a file")
	fromStdin := fs.Bool("stdin", false, "read the message from standard input")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return usageErr{errors.New("usage: wi send ITEM [--wait] [--follow-up] [--file PATH | --stdin | MESSAGE...]")}
	}
	id, err := rpcSelect(ctx, cfg, fs.Arg(0))
	if err != nil {
		return err
	}
	message, err := readAgentControlMessage(cfg, fs.Args()[1:], *file, *fromStdin)
	if err != nil {
		return usageErr{err}
	}
	delivery := v1.Delivery_DELIVERY_UNSPECIFIED
	if *followUp {
		delivery = v1.Delivery_DELIVERY_FOLLOW_UP
	}
	clientMessage := newCommandID()
	value, err := cfg.API.Agents.SendMessage(ctx, rpcRequest(cfg, &v1.SendMessageRequest{ItemId: id, Text: message, Delivery: delivery, ClientMessageId: clientMessage, StartMode: v1.AgentMode_AGENT_MODE_HEADLESS}))
	if err != nil {
		return err
	}
	if value.Msg.Job, err = rpcWaitJob(ctx, cfg, value.Msg.Job, jsonOut); err != nil {
		return err
	}
	if !*wait {
		if jsonOut {
			return rpcEmit(cfg, true, value.Msg)
		}
		fmt.Fprintf(cfg.Stdout, "sent to %s\n", id)
		return nil
	}
	answer, err := awaitAnswer(ctx, cfg, id, clientMessage, *timeout)
	if err != nil {
		return err
	}
	if jsonOut {
		return rpcEmit(cfg, true, answer)
	}
	fmt.Fprintln(cfg.Stdout, partsText(answer.Parts))
	return nil
}

// awaitAnswer waits until the message is in the item's history and its
// agent has settled, then returns the agent's last reply after it. A turn
// that settled without a reply is reported only once the agent was seen
// working: before that, the message may simply not have started one yet.
func awaitAnswer(ctx context.Context, cfg Config, itemID, clientMessage string, timeout time.Duration) (*v1.ConversationMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	worked := false
	for {
		agent, err := cfg.API.Agents.GetAgent(ctx, rpcRequest(cfg, &v1.GetAgentRequest{ItemId: itemID}))
		if err != nil {
			return nil, err
		}
		if agent.Msg.Agent.GetStatus() == v1.AgentStatus_AGENT_STATUS_PROBLEM {
			return nil, fmt.Errorf("the agent of %s has a problem: %s", itemID, agent.Msg.Agent.GetReason())
		}
		if agent.Msg.Agent.GetStatus() == v1.AgentStatus_AGENT_STATUS_BUSY {
			worked = true
		} else {
			history, err := cfg.API.Agents.GetHistory(ctx, rpcRequest(cfg, &v1.GetHistoryRequest{ItemId: itemID}))
			if err != nil {
				return nil, err
			}
			if answer, found := answerAfter(history.Msg.Messages, clientMessage); found {
				if answer != nil {
					return answer, nil
				}
				if worked {
					return nil, fmt.Errorf("the agent of %s settled without answering", itemID)
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("no answer from %s within %s", itemID, timeout)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// answerAfter finds the message sent with clientMessage and the last
// assistant reply with text after it.
func answerAfter(messages []*v1.ConversationMessage, clientMessage string) (*v1.ConversationMessage, bool) {
	for i, m := range messages {
		if m.ClientMessageId != clientMessage {
			continue
		}
		var answer *v1.ConversationMessage
		for _, later := range messages[i+1:] {
			if later.Role == v1.MessageRole_MESSAGE_ROLE_ASSISTANT && strings.TrimSpace(partsText(later.Parts)) != "" {
				answer = later
			}
		}
		return answer, true
	}
	return nil, false
}

func partsText(parts []*v1.Part) string {
	var b strings.Builder
	for _, part := range parts {
		if text := part.GetText(); text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(text)
		}
	}
	return b.String()
}
