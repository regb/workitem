package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// TestSendingToAStoppedAgentAsksForApprovalsItsStartNeeds: a message to a
// stopped agent starts it in a job, which asks for an approval such as
// trusting a new worktree .envrc instead of failing the send.
func TestSendingToAStoppedAgentAsksForApprovalsItsStartNeeds(t *testing.T) {
	if _, err := exec.LookPath("direnv"); err != nil {
		t.Skip("direnv is not installed")
	}
	f := newBinaryFixture(t)
	ctx := context.Background()
	var repo v1.AddRepositoryResponse
	f.json(&repo, "repo", "add", "test", "--from", f.source, "--local")
	started, err := f.clients.Lifecycle.CreateAndStartItem(ctx, connect.NewRequest(&v1.CreateAndStartItemRequest{Item: &v1.CreateItemRequest{RepositoryId: repo.Repository.Id, Title: "Approval"}, Start: &v1.StartOptions{Prompt: "first prompt", Mode: v1.AgentMode_AGENT_MODE_HEADLESS}}))
	if err != nil {
		t.Fatal(err)
	}
	item := started.Msg.Item.Id
	f.historyReady(item, 2)
	f.idle(item)
	if _, err := f.clients.Agents.StopAgent(ctx, connect.NewRequest(&v1.StopAgentRequest{ItemId: item, Force: true})); err != nil {
		t.Fatal(err)
	}
	value, err := f.clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: item}))
	if err != nil {
		t.Fatal(err)
	}
	// Nobody controls the stopped agent, as after a scheduled start.
	if control, err := f.clients.Control.GetControl(ctx, connect.NewRequest(&v1.GetControlRequest{ItemId: item})); err != nil {
		t.Fatal(err)
	} else if lease := control.Msg.Control.Lease; lease != nil {
		if _, err := f.clients.Control.ReleaseControl(ctx, connect.NewRequest(&v1.ReleaseControlRequest{LeaseId: lease.Id})); err != nil {
			t.Fatal(err)
		}
	}
	checkout := value.Msg.Item.Checkout.Claim.Path
	if err := os.WriteFile(filepath.Join(checkout, ".envrc"), []byte("export WI_APPROVAL_TEST=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var sent *connect.Response[v1.SendMessageResponse]
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		// Sent as the browser sends it: a web client, which takes control.
		request := connect.NewRequest(&v1.SendMessageRequest{ItemId: item, Text: "after approval", ClientMessageId: "approval-1", StartMode: v1.AgentMode_AGENT_MODE_HEADLESS})
		request.Header().Set("Wi-Client-Id", "browser-1")
		request.Header().Set("Wi-Client-Kind", "web")
		sent, err = f.clients.Agents.SendMessage(ctx, request)
		// The agent may still be stopping.
		if err == nil || time.Now().After(deadline) || strings.Contains(err.Error(), "envrc") {
			break
		}
	}
	if err != nil || sent.Msg.Job == nil {
		t.Fatalf("send to a stopped agent: %v %v", sent, err)
	}
	var job *v1.Job
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		value, err := f.clients.Jobs.GetJob(ctx, connect.NewRequest(&v1.GetJobRequest{JobId: sent.Msg.Job.Id}))
		if err != nil {
			t.Fatal(err)
		}
		job = value.Msg.Job
		if job.State == v1.JobState_JOB_STATE_WAITING_FOR_APPROVAL || job.State == v1.JobState_JOB_STATE_FAILED || job.State == v1.JobState_JOB_STATE_SUCCEEDED || time.Now().After(deadline) {
			break
		}
	}
	if job.State != v1.JobState_JOB_STATE_WAITING_FOR_APPROVAL || job.Approval == nil || job.Approval.Question != "Allow worktree .envrc?" {
		t.Fatalf("job did not ask for the .envrc: %v", job)
	}
	// Denied, the agent starts without the .envrc and gets the message.
	if _, err := f.clients.Jobs.AnswerApproval(ctx, connect.NewRequest(&v1.AnswerApprovalRequest{JobId: job.Id, ChallengeId: job.Approval.ChallengeId, Decision: v1.ApprovalDecision_APPROVAL_DECISION_DENY})); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		value, err := f.clients.Jobs.GetJob(ctx, connect.NewRequest(&v1.GetJobRequest{JobId: job.Id}))
		if err != nil {
			t.Fatal(err)
		}
		job = value.Msg.Job
		if job.State == v1.JobState_JOB_STATE_SUCCEEDED {
			break
		}
		if job.State == v1.JobState_JOB_STATE_FAILED || time.Now().After(deadline) {
			t.Fatalf("send job: %v", job)
		}
	}
	f.idle(item)
	history, err := f.clients.Agents.GetHistory(ctx, connect.NewRequest(&v1.GetHistoryRequest{ItemId: item}))
	if err != nil {
		t.Fatal(err)
	}
	delivered := false
	for _, message := range history.Msg.Messages {
		delivered = delivered || message.Role == v1.MessageRole_MESSAGE_ROLE_USER && message.Text == "after approval"
	}
	if !delivered {
		t.Fatalf("message not delivered: %v", history.Msg.Messages)
	}
}
