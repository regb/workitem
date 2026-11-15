package daemonapi

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

func TestQueuedMessageReceiptWaitsForNativeOutcome(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	created, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: f.repo(t), Title: "delivery"}))
	if err != nil {
		t.Fatal(err)
	}
	item := created.Msg.Item.Id
	if err := f.api.App.Store.SaveAgentRuntime(ctx, item, model.AgentRuntime{ID: "runtime", WorkItemID: item, Generation: 1}); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"not_sent", "cancelled", "uncertain", "confirmed"} {
		t.Run(state, func(t *testing.T) {
			receipt := coordinator.APIMessageReceipt{ItemID: item, MessageID: state, RuntimeID: "runtime", Generation: 1, CreatedAt: time.Now().UTC(), Accepted: true}
			if err := f.core.Database().RecordAPIMessage(receipt); err != nil {
				t.Fatal(err)
			}
			commandID := messageCommandID(item, state)
			emit := func(kind, delivery, reason string) {
				t.Helper()
				_, err := coordinator.IngestRuntimeEvent(ctx, f.core.Database(), coordinator.RuntimeSemanticEvent{ID: fmt.Sprintf("%s-%s", state, delivery), WorkItemID: item, RuntimeID: "runtime", CommandID: commandID, RequestID: state, Type: kind, DeliveryState: delivery, ReasonCode: reason, Timestamp: time.Now().UTC(), Failed: kind == "command.rejected"})
				if err != nil {
					t.Fatal(err)
				}
			}
			emit("queue.changed", "queued", "queued_startup")
			done, err := f.api.messageReceiptResult(receipt)
			if err != nil || done {
				t.Fatalf("queue was delivered: %v %v", done, err)
			}
			waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- f.api.waitMessageConfirmation(waitCtx, item, "runtime", commandID) }()
			select {
			case err := <-result:
				t.Fatalf("queue prematurely completed: %v", err)
			case <-time.After(60 * time.Millisecond):
			}
			kind, reason := "command.rejected", "pane_changed"
			if state == "confirmed" {
				kind, reason = "command.accepted", "native_confirmed"
			} else if state == "uncertain" {
				reason = "confirmation_timeout"
			}
			emit(kind, state, reason)
			err = <-result
			if (err == nil) != (state == "confirmed") {
				t.Fatalf("outcome: %s %v", state, err)
			}
			got, found, err := f.core.Database().APIMessage(item, state)
			if err != nil || !found || got.Delivered || !got.Accepted {
				t.Fatalf("queue persisted a false delivered receipt: %+v %v", got, err)
			}
			for i := 0; i < 2; i++ {
				if i == 1 {
					// Neither queue acceptance nor a legacy transport-only
					// delivered receipt may conceal the native outcome.
					receipt.Delivered = true
					if err := f.core.Database().RecordAPIMessage(receipt); err != nil {
						t.Fatal(err)
					}
				}
				response, err := f.clients.Agents.SendMessage(ctx, connect.NewRequest(&v1.SendMessageRequest{ItemId: item, Text: "original task", ClientMessageId: state}))
				if state == "confirmed" {
					if err != nil || response.Msg.Generation != 1 {
						t.Fatalf("confirmed retry: %v %v", response, err)
					}
				} else if err == nil || !strings.Contains(err.Error(), map[string]string{"not_sent": "not sent", "cancelled": "not sent", "uncertain": "uncertain"}[state]) {
					t.Fatalf("retry concealed failure: %v", err)
				}
			}
		})
	}
}
