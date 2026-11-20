package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// agentQuestion is an AskUserQuestion call's input.
type agentQuestion struct {
	Question    string `json:"question"`
	Header      string `json:"header"`
	MultiSelect bool   `json:"multiSelect"`
	Options     []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"options"`
}

// pendingQuestion reads the question the agent waits on from its live
// content, where its driver publishes it while it can be answered.
func pendingQuestion(ctx context.Context, cfg Config, id string) (string, []agentQuestion, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stream, err := cfg.API.Agents.WatchLiveContent(ctx, rpcRequest(cfg, &v1.WatchLiveContentRequest{ItemId: id}))
	if err != nil {
		return "", nil, err
	}
	defer stream.Close()
	if !stream.Receive() {
		if err := stream.Err(); err != nil {
			return "", nil, err
		}
		return "", nil, errors.New("the agent's live content is unavailable")
	}
	question := stream.Msg().GetSnapshot().GetQuestion()
	if question == nil {
		agent, err := cfg.API.Agents.GetAgent(ctx, rpcRequest(cfg, &v1.GetAgentRequest{ItemId: id}))
		if err == nil && agent.Msg.Agent.GetAsking() {
			return "", nil, errors.New("the agent asks in its terminal; answer it there")
		}
		return "", nil, errors.New("the agent is not waiting for an answer")
	}
	var input struct {
		Questions []agentQuestion `json:"questions"`
	}
	if err := json.Unmarshal([]byte(question.Input), &input); err != nil || len(input.Questions) == 0 {
		return "", nil, errors.New("the agent's question could not be read")
	}
	return question.Id, input.Questions, nil
}

// answerText turns an answer given on the command line into the agent's
// answer: option numbers or labels (comma-separated for a multi-select
// question) become labels; anything else is the operator's own text.
func answerText(q agentQuestion, given string) string {
	given = strings.TrimSpace(given)
	pick := func(part string) (string, bool) {
		part = strings.TrimSpace(part)
		if n, err := strconv.Atoi(part); err == nil && n >= 1 && n <= len(q.Options) {
			return q.Options[n-1].Label, true
		}
		for _, option := range q.Options {
			if strings.EqualFold(option.Label, part) {
				return option.Label, true
			}
		}
		return "", false
	}
	if label, ok := pick(given); ok {
		return label
	}
	if q.MultiSelect && strings.Contains(given, ",") {
		labels := []string{}
		for _, part := range strings.Split(given, ",") {
			label, ok := pick(part)
			if !ok {
				return given
			}
			labels = append(labels, label)
		}
		return strings.Join(labels, ", ")
	}
	return given
}

func rpcAgentAnswer(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("agent answer", cfg.Stderr)
	selected := fs.String("item", "", "item")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	// Arguments are answers, so the item comes only from --item or context.
	id, err := rpcSelect(ctx, cfg, *selected)
	if err != nil {
		return err
	}
	toolCall, questions, err := pendingQuestion(ctx, cfg, id)
	if err != nil {
		return err
	}
	given := fs.Args()
	if len(given) == 0 {
		if jsonOut {
			return json.NewEncoder(cfg.Stdout).Encode(map[string]any{"tool_call_id": toolCall, "questions": questions})
		}
		for i, q := range questions {
			kind := ""
			if q.MultiSelect {
				kind = " (several, comma-separated)"
			}
			fmt.Fprintf(cfg.Stdout, "%d. %s%s\n", i+1, q.Question, kind)
			for n, option := range q.Options {
				fmt.Fprintf(cfg.Stdout, "   %d) %s", n+1, option.Label)
				if option.Description != "" {
					fmt.Fprintf(cfg.Stdout, " - %s", option.Description)
				}
				fmt.Fprintln(cfg.Stdout)
			}
		}
		fmt.Fprintln(cfg.Stdout, "Answer with wi agent answer ANSWER..., one per question: an option number or label, or your own text.")
		return nil
	}
	if len(given) != len(questions) {
		return usageErr{fmt.Errorf("the agent asks %d question(s); give one answer for each", len(questions))}
	}
	answers := map[string]string{}
	for i, q := range questions {
		answers[q.Question] = answerText(q, given[i])
	}
	value, err := cfg.API.Agents.AnswerQuestion(ctx, rpcRequest(cfg, &v1.AnswerQuestionRequest{ItemId: id, ToolCallId: toolCall, Answers: answers}))
	if err != nil {
		return err
	}
	if jsonOut {
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	for _, q := range questions {
		fmt.Fprintf(cfg.Stdout, "%s → %s\n", q.Question, answers[q.Question])
	}
	return nil
}
