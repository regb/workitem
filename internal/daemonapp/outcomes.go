package daemonapp

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/coordinator"
	"time"
)

type coordinatorControlOutcomes struct{ client *coordinator.Client }

func (o coordinatorControlOutcomes) Cursor(ctx context.Context, itemID string) (uint64, error) {
	result, err := o.client.RuntimeEvents(ctx, coordinator.RuntimeEventsRequest{ItemID: itemID, Limit: 1})
	return result.LastSequence, err
}

func (o coordinatorControlOutcomes) Wait(ctx context.Context, itemID string, cursor uint64, commandID string, timeout time.Duration) (bool, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		result, err := o.client.RuntimeEvents(ctx, coordinator.RuntimeEventsRequest{ItemID: itemID, AfterSequence: cursor, Limit: 256, WaitMillis: 1000})
		if err != nil {
			if ctx.Err() != nil {
				return false, "", errors.New("no answer in time; the change may still apply")
			}
			return false, "", err
		}
		for _, event := range result.Events {
			var payload struct {
				CommandID  string `json:"command_id"`
				ReasonCode string `json:"reason_code"`
			}
			if json.Unmarshal(event.Payload, &payload) != nil || payload.CommandID != commandID {
				continue
			}
			switch event.Type {
			case agent.EventCommandAccepted:
				return true, "", nil
			case agent.EventCommandRejected:
				return false, "the agent rejected the change", nil
			}
		}
		cursor = result.LastSequence
	}
}
