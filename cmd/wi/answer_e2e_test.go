package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// TestAnsweringTheAgentsQuestion: an agent that asks a question waits under
// NEEDS ATTENTION, and wi agent answer shows the question and answers it.
func TestAnsweringTheAgentsQuestion(t *testing.T) {
	f := newBinaryFixture(t)
	ctx := context.Background()
	var repo v1.AddRepositoryResponse
	f.json(&repo, "repo", "add", "test", "--from", f.source, "--local")
	var started v1.CreateAndStartItemResponse
	f.json(&started, "new", "--repo", repo.Repository.Id, "--title", "Questions", "--start", "--agent-mode", "headless", "--prompt", "first prompt")
	item := started.Item.Id
	f.historyReady(item, 2)
	agent := f.idle(item)
	if agent.Capabilities == nil || !agent.Capabilities.Answer {
		t.Fatalf("capabilities: %v", agent.Capabilities)
	}
	if out, err := f.command("agent", "answer", "--item", item); err == nil || !strings.Contains(string(out), "not waiting") {
		t.Fatalf("answer without a question: %v %s", err, out)
	}

	f.run("agent", "control", "send", "--item", item, "please ask me something")
	var observation *v1.ItemObservation
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		value, err := f.clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: item}))
		if err != nil {
			t.Fatal(err)
		}
		observation = value.Msg.Observation
		if observation != nil && observation.Asking || time.Now().After(deadline) {
			break
		}
	}
	if observation == nil || !observation.Asking || !observation.NeedsAttention {
		t.Fatalf("a question needs attention: %v", observation)
	}
	shown := string(f.run("agent", "answer", "--item", item))
	if !strings.Contains(shown, "Which color?") || !strings.Contains(shown, "2) Blue - Cool") {
		t.Fatalf("question shown:\n%s", shown)
	}
	if out, err := f.command("agent", "answer", "--item", item, "Blue", "extra"); err == nil || !strings.Contains(string(out), "give one answer for each") {
		t.Fatalf("too many answers: %v %s", err, out)
	}
	answered := string(f.run("agent", "answer", "--item", item, "2"))
	if !strings.Contains(answered, "Which color? → Blue") {
		t.Fatalf("answer: %s", answered)
	}
	// The agent continues with the answer.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		history, err := f.clients.Agents.GetHistory(ctx, connect.NewRequest(&v1.GetHistoryRequest{ItemId: item}))
		if err != nil {
			t.Fatal(err)
		}
		if last := history.Msg.Messages[len(history.Msg.Messages)-1]; messageText(last) == "Chosen: Blue" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the agent did not get the answer: %v", history.Msg.Messages)
		}
	}
	f.idle(item)
	if out, err := f.command("agent", "answer", "--item", item, "Red"); err == nil || !strings.Contains(string(out), "not waiting") {
		t.Fatalf("answering again: %v %s", err, out)
	}
}
