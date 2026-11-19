package daemonapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/lock"
	"github.com/regb/workitem/internal/model"
)

func waitReviewRecord(t *testing.T, f *fixture, item string, want func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !want() {
		if time.Now().After(deadline) {
			state, err := f.core.LoadReview(context.Background(), item)
			t.Fatalf("capture did not finish: %+v %v", state, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func TestAutomaticReviewCapturesEveryModeAndRetainsNonblockingFailures(t *testing.T) {
	for _, mode := range []string{"tui", "headless"} {
		t.Run(mode, func(t *testing.T) {
			f, item, ws := reviewWorkspace(t)
			ctx := context.Background()
			claim := ws.Claim
			runtime := model.AgentRuntime{ID: "runtime", WorkItemID: item, ConversationID: "conversation", Mode: mode, State: model.AgentRuntimeRunning, WorkspaceClaimID: claim.Id, WorkspaceClaimGeneration: uint64(claim.Generation), StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
			if err := f.api.App.Store.SaveAgentRuntime(ctx, item, runtime); err != nil {
				t.Fatal(err)
			}
			// No driver capability or supported-version opt-in exists. Native terminal
			// and headless settlements enter the same daemon-side app use case.
			if err := os.WriteFile(filepath.Join(claim.Path, "code.txt"), []byte("settled code\n"), 0600); err != nil {
				t.Fatal(err)
			}
			event := coordinator.RuntimeSemanticEvent{ID: "settled-one", RuntimeID: runtime.ID, WorkItemID: item, Type: "agent.settled", Timestamp: time.Now().UTC()}
			if _, err := f.core.LocalClient().IngestRuntimeEvent(ctx, event); err != nil {
				t.Fatal(err)
			}
			waitReviewRecord(t, f, item, func() bool { state, _ := f.core.LoadReview(ctx, item); return len(state.Revisions) == 1 })
			state, _ := f.core.LoadReview(ctx, item)
			if state.Revisions[0].Trigger != "settled" || state.Revisions[0].Base != state.Revisions[0].Head {
				t.Fatal("captured pre-execution baseline instead of first observed HEAD")
			}
			if _, err := f.core.LocalClient().IngestRuntimeEvent(ctx, event); err != nil {
				t.Fatal(err)
			}
			time.Sleep(30 * time.Millisecond)
			state, _ = f.core.LoadReview(ctx, item)
			if len(state.Revisions) != 1 {
				t.Fatal("duplicate native event caused another capture")
			}
			gitdir := integrationGit(t, claim.Path, "rev-parse", "--absolute-git-dir")
			if err := os.WriteFile(filepath.Join(gitdir, "MERGE_HEAD"), []byte(state.Revisions[0].Head+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			defer os.Remove(filepath.Join(gitdir, "MERGE_HEAD"))
			event.ID = "settled-failure"
			event.Timestamp = time.Now().UTC()
			event.Outcome = "failed"
			event.Failed = true
			if _, err := f.core.LocalClient().IngestRuntimeEvent(ctx, event); err != nil {
				t.Fatal("capture failure rejected agent settlement", err)
			}
			waitReviewRecord(t, f, item, func() bool { state, _ := f.core.LoadReview(ctx, item); return len(state.Gaps) == 1 })
			state, _ = f.core.LoadReview(ctx, item)
			if state.Gaps[0].Reason == "" {
				t.Fatal("missing visible capture failure reason")
			}
			if err := os.Remove(filepath.Join(gitdir, "MERGE_HEAD")); err != nil {
				t.Fatal(err)
			}
			event.ID = "settled-after-failure"
			event.Timestamp = time.Now().UTC()
			if _, err := f.core.LocalClient().IngestRuntimeEvent(ctx, event); err != nil {
				t.Fatal(err)
			}
			waitReviewRecord(t, f, item, func() bool { state, _ := f.core.LoadReview(ctx, item); return len(state.Revisions) == 2 })
		})
	}
}
func TestSettlementWithoutCheckoutSilentlySkipsReviewCapture(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	repo := f.repo(t)
	created, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "No checkout"}))
	if err != nil {
		t.Fatal(err)
	}
	f.api.App.CaptureAfterSettlement(ctx, created.Msg.Item.Id)
	_, found, err := f.core.Database().ReadReviewRecord(created.Msg.Item.Id)
	if err != nil || found {
		t.Fatal("absent checkout created review noise", err)
	}
}
func TestGitCaptureDoesNotHoldItemOrControllerAdmission(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	f, item, ws := reviewWorkspace(t)
	ctx := context.Background()
	body, _ := json.Marshal(coordinator.AgentObservation{WorkItemID: item, Status: "idle"})
	if _, err := f.core.Database().Commit(coordinator.Command{ID: "idle-observation", Type: "test", ItemID: item}, nil, []coordinator.ProjectionUpdate{{Projection: coordinator.AgentObservationProjection, Key: item, Value: body}}); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	started := filepath.Join(bin, "started")
	release := filepath.Join(bin, "release")
	// Pause actual Git object creation rather than the record transaction.
	script := fmt.Sprintf("#!/bin/sh\nfor arg in \"$@\"; do if [ \"$arg\" = hash-object ]; then touch '%s'; while [ ! -e '%s' ]; do sleep .01; done; break; fi; done\nexec '%s' \"$@\"\n", started, release, realGit)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(ws.Claim.Path, "code.txt"), []byte("code\n"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.clients.Reviews.CaptureReviewRevision(ctx, connect.NewRequest(&v1.CaptureReviewRevisionRequest{ItemId: item, CaptureId: "paused", ExpectedClaimId: ws.Claim.Id, ExpectedClaimGeneration: ws.Claim.Generation}))
		done <- err
	}()
	defer func() {
		_ = os.WriteFile(release, nil, 0600)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("capture did not stop")
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Git did not reach paused capture")
		}
		time.Sleep(5 * time.Millisecond)
	}
	status, err := f.clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: item}))
	if err != nil || !status.Msg.Observation.Capturing {
		t.Fatal("capture observation unavailable while Git runs", err)
	}
	list, err := f.clients.Items.ListItems(ctx, connect.NewRequest(&v1.ListItemsRequest{Order: v1.ListOrder_LIST_ORDER_ATTENTION}))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range list.Msg.Items {
		if row.Id == item {
			found = true
			if !row.Observation.Capturing || row.Observation.NeedsAttention || row.AttentionGroup != v1.AttentionGroup_ATTENTION_GROUP_WORKING || row.Observation.AgentStatus != v1.AgentStatus_AGENT_STATUS_IDLE {
				t.Fatal("capture changed agent status or attention grouping", row)
			}
		}
	}
	if !found {
		t.Fatal("capturing item missing from list")
	}
	admission, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	guard, err := lock.AcquireOperation(admission, f.api.App.Store.LockPath(item))
	if err != nil {
		t.Fatal("capture blocked item/control admission", err)
	}
	guard.Release()
	// A send is refused immediately for the ordinary reason (no running agent),
	// not held until capture finishes.
	_, err = f.clients.Agents.SendMessage(admission, connect.NewRequest(&v1.SendMessageRequest{ItemId: item, Text: "normal send", ClientMessageId: "send", Delivery: v1.Delivery_DELIVERY_PROMPT}))
	if admission.Err() != nil {
		t.Fatal("send was held by capture", err)
	}
}
