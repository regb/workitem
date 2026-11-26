package daemonapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/daemonapi"
)

func TestLegacyMessageReceiptUsesRetainedNativeOutcome(t *testing.T) {
	f := apitest.New(t)
	ctx := context.Background()
	created, err := f.Clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: f.Repository(t, "test"), Title: "legacy delivery"}))
	if err != nil {
		t.Fatal(err)
	}
	item := created.Msg.Item.Id
	for _, tc := range []struct {
		name           string
		states         []string
		foreignRuntime bool
		foreignCommand bool
		done           bool
		errorText      string
	}{
		{name: "not-sent", states: []string{"queued", "not_sent"}, errorText: "not sent"},
		{name: "cancelled", states: []string{"queued", "cancelled"}, errorText: "not sent"},
		{name: "uncertain", states: []string{"submitted", "uncertain"}, errorText: "uncertain"},
		{name: "confirmed", states: []string{"queued", "confirmed", "uncertain"}, done: true},
		{name: "terminal-rejection", states: []string{"not_sent", "queued"}, errorText: "not sent"},
		{name: "queued", states: []string{"queued"}},
		{name: "unobserved", done: true},
		{name: "foreign-runtime", states: []string{"not_sent"}, foreignRuntime: true, done: true},
		{name: "foreign-command", states: []string{"not_sent"}, foreignCommand: true, done: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UTC()
			receipt := coordinator.APIMessageReceipt{ItemID: item, MessageID: tc.name, RuntimeID: "old-runtime", Generation: 1, CreatedAt: now, Delivered: true}
			if err := f.Core().Database().RecordAPIMessage(receipt); err != nil {
				t.Fatal(err)
			}
			for i, state := range tc.states {
				runtime, command := "old-runtime", daemonapi.MessageCommandID(item, tc.name)
				if tc.foreignRuntime {
					runtime = "replacement"
				}
				if tc.foreignCommand {
					command = "unrelated-command"
				}
				kind := "queue.changed"
				if state == "confirmed" {
					kind = "command.accepted"
				} else if state == "not_sent" || state == "cancelled" || state == "uncertain" {
					kind = "command.rejected"
				}
				payload, _ := json.Marshal(map[string]any{"runtime_id": runtime, "command_id": command, "delivery_state": state, "reason_code": "pane_changed"})
				eventID := fmt.Sprintf("legacy-%s-%d", tc.name, i)
				// The previous ingester retained these facts without the new projection.
				_, err := f.Core().Database().Commit(coordinator.Command{ID: eventID, ProtocolVersion: coordinator.ProtocolVersion, Type: "runtime.event.ingest", Actor: "runtime", ItemID: item, ReceivedAt: now}, []coordinator.PendingEvent{{ID: eventID, ItemID: item, Type: kind, Timestamp: now.Add(time.Duration(i) * time.Millisecond), Actor: "runtime", Payload: payload}}, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			indexed, err := f.Core().Database().CommandDelivery(item, "old-runtime", daemonapi.MessageCommandID(item, tc.name))
			if err != nil || indexed.State != "" {
				t.Fatal("fixture unexpectedly populated the new projection", indexed, err)
			}
			done, err := f.API().MessageReceiptResult(receipt)
			if done != tc.done || (tc.errorText == "" && err != nil) || (tc.errorText != "" && (err == nil || !strings.Contains(err.Error(), tc.errorText))) {
				t.Fatalf("legacy result: done=%v err=%v", done, err)
			}
			if tc.errorText != "" {
				_, err = f.Clients.Agents.SendMessage(ctx, connect.NewRequest(&v1.SendMessageRequest{ItemId: item, ClientMessageId: tc.name, Text: "original task"}))
				if err == nil || !strings.Contains(err.Error(), tc.errorText) {
					t.Fatalf("API concealed retained failure: %v", err)
				}
			}
		})
	}
}
