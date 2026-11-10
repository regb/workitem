package coordinator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

const commandDeliveryProjection = "command_delivery"

// CommandDelivery is an observation of a handoff, not its native content.
// It is fenced by runtime and command identity so a replacement cannot inherit it.
type CommandDelivery struct {
	State      string    `json:"state"`
	Reason     string    `json:"reason"`
	ObservedAt time.Time `json:"observed_at"`
}

func commandDeliveryKey(itemID, runtimeID, commandID string) string {
	sum := sha256.Sum256([]byte(itemID + "\x00" + runtimeID + "\x00" + commandID))
	return hex.EncodeToString(sum[:])
}

func (d *Database) CommandDelivery(itemID, runtimeID, commandID string) (CommandDelivery, error) {
	var value CommandDelivery
	_, err := d.ReadProjection(commandDeliveryProjection, commandDeliveryKey(itemID, runtimeID, commandID), &value)
	return value, err
}

func commandDeliveryUpdate(d *Database, event RuntimeSemanticEvent) ([]ProjectionUpdate, error) {
	switch event.Type {
	case "queue.changed", "command.accepted", "command.rejected":
	default:
		return nil, nil
	}
	if event.CommandID == "" || event.DeliveryState == "" {
		return nil, nil
	}
	old, err := d.CommandDelivery(event.WorkItemID, event.RuntimeID, event.CommandID)
	if err != nil {
		return nil, err
	}
	next, changed := advanceCommandDelivery(old, event)
	if !changed {
		return nil, nil
	}
	value, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	return []ProjectionUpdate{{Projection: commandDeliveryProjection, Key: commandDeliveryKey(event.WorkItemID, event.RuntimeID, event.CommandID), Value: value}}, nil
}

func advanceCommandDelivery(old CommandDelivery, event RuntimeSemanticEvent) (CommandDelivery, bool) {
	// Native confirmation is conclusive, even if a delayed timeout arrives later.
	terminal := old.State == "not_sent" || old.State == "cancelled" || old.State == "uncertain"
	if old.State == "confirmed" || event.Timestamp.Before(old.ObservedAt) || terminal && event.DeliveryState != "confirmed" {
		return old, false
	}
	return CommandDelivery{State: event.DeliveryState, Reason: event.ReasonCode, ObservedAt: event.Timestamp}, true
}

// LegacyCommandDelivery recovers facts retained before the delivery projection
// existed. Only legacy receipts need this scan; normal ingestion and delivery
// continue to use the indexed projection.
func (d *Database) LegacyCommandDelivery(itemID, runtimeID, commandID string) (CommandDelivery, error) {
	events, err := d.ListItemEvents(itemID)
	if err != nil {
		return CommandDelivery{}, err
	}
	var value CommandDelivery
	for _, event := range events {
		switch event.Type {
		case "queue.changed", "command.accepted", "command.rejected":
		default:
			continue
		}
		var facts RuntimeSemanticEvent
		if err := json.Unmarshal(event.Payload, &facts); err != nil {
			return CommandDelivery{}, fmt.Errorf("read legacy command delivery: %w", err)
		}
		if facts.RuntimeID != runtimeID || facts.CommandID != commandID || facts.DeliveryState == "" {
			continue
		}
		facts.Timestamp = event.Timestamp
		value, _ = advanceCommandDelivery(value, facts)
	}
	return value, nil
}
