package coordinator

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
)

func TestCommandDeliverySurvivesRestartAndRejectsStaleOwners(t *testing.T) {
	now := time.Now().UTC()
	m := model.NewManifest(importTestID, "delivery", "Delivery", nil, false, now, model.Repository{}, model.Checkout{})
	db, root := itemDatabase(t, m)
	seedRuntimeOwner(t, db, m.ID, "runtime")
	event := RuntimeSemanticEvent{WorkItemID: m.ID, RuntimeID: "runtime", CommandID: "command", Type: "queue.changed", RequestID: "request"}
	for i, state := range []string{"queued", "submitted", "not_sent", "queued"} {
		event.ID = fmt.Sprint("delivery-", i)
		event.Timestamp = now.Add(time.Duration(i) * time.Second)
		event.DeliveryState = state
		if state == "not_sent" {
			event.Type = "command.rejected"
			event.ReasonCode = "pane_changed"
			event.Failed = true
		}
		if _, err := IngestRuntimeEvent(context.Background(), db, event); err != nil {
			t.Fatal(err)
		}
	}
	value, err := db.CommandDelivery(m.ID, "runtime", "command")
	if err != nil || value.State != "not_sent" || value.Reason != "pane_changed" {
		t.Fatalf("rejection regressed: %+v %v", value, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = OpenDatabase(filepath.Clean(root))
	if err != nil {
		t.Fatal(err)
	}
	value, err = db.CommandDelivery(m.ID, "runtime", "command")
	if err != nil || value.State != "not_sent" {
		t.Fatalf("lost rejection: %+v %v", value, err)
	}
	value, err = db.CommandDelivery(m.ID, "replacement", "command")
	if err != nil || value.State != "" {
		t.Fatal("delivery crossed generations", value, err)
	}
	event.ID = "confirmation"
	event.Timestamp = now.Add(5 * time.Second)
	event.Type = "command.accepted"
	event.DeliveryState = "confirmed"
	event.ReasonCode = "native_confirmed"
	event.Failed = false
	if _, err := IngestRuntimeEvent(context.Background(), db, event); err != nil {
		t.Fatal(err)
	}
	event.ID = "delayed-timeout"
	event.Timestamp = now.Add(6 * time.Second)
	event.Type = "command.rejected"
	event.DeliveryState = "uncertain"
	event.ReasonCode = "confirmation_timeout"
	event.Failed = true
	if _, err := IngestRuntimeEvent(context.Background(), db, event); err != nil {
		t.Fatal(err)
	}
	value, err = db.CommandDelivery(m.ID, "runtime", "command")
	if err != nil || value.State != "confirmed" {
		t.Fatal("native confirmation regressed", value, err)
	}
	seedRuntimeOwner(t, db, m.ID, "replacement")
	event.ID = "stale-owner"
	if _, err := IngestRuntimeEvent(context.Background(), db, event); err == nil {
		t.Fatal("stale owner changed delivery")
	}
}
