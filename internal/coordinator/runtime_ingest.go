package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/model"
)

func safeRuntimeID(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, `/\\`) && len(value) <= 200
}

type RuntimeSemanticEvent struct {
	ID         string    `json:"id"`
	RuntimeID  string    `json:"runtime_id"`
	WorkItemID string    `json:"work_item_id"`
	Type       string    `json:"type"`
	Timestamp  time.Time `json:"timestamp"`
	RequestID  string    `json:"request_id,omitempty"`
	Role       string    `json:"role,omitempty"`
	ToolName   string    `json:"tool_name,omitempty"`
	Failed     bool      `json:"failed,omitempty"`
}

type RuntimeOwnerMismatchError struct {
	WorkItemID       string
	EventRuntimeID   string
	CurrentRuntimeID string
}

func (e *RuntimeOwnerMismatchError) Error() string {
	if e.CurrentRuntimeID == "" {
		return fmt.Sprintf("runtime %s does not own work item %s", e.EventRuntimeID, e.WorkItemID)
	}
	return fmt.Sprintf("runtime %s does not own work item %s; current owner is %s", e.EventRuntimeID, e.WorkItemID, e.CurrentRuntimeID)
}

type RuntimeIngestResult struct {
	EventID      string `json:"event_id"`
	Sequence     uint64 `json:"sequence"`
	ItemRevision uint64 `json:"item_revision"`
	Duplicate    bool   `json:"duplicate,omitempty"`
}

func IngestRuntimeEvent(ctx context.Context, database *Database, event RuntimeSemanticEvent) (RuntimeIngestResult, error) {
	if err := ctx.Err(); err != nil {
		return RuntimeIngestResult{}, err
	}
	if event.ID == "" || compactIdentifier(event.ID, 200) != event.ID || !safeRuntimeID(event.RuntimeID) || !model.ValidID(event.WorkItemID) {
		return RuntimeIngestResult{}, errors.New("valid event, runtime, and work-item ids are required")
	}
	if event.Timestamp.IsZero() {
		return RuntimeIngestResult{}, errors.New("runtime event timestamp is required")
	}
	if !allowedLiveRuntimeEvent(event.Type) {
		return RuntimeIngestResult{}, fmt.Errorf("runtime event type %q is not retained", event.Type)
	}
	if _, err := database.CanonicalManifest(event.WorkItemID); err != nil {
		return RuntimeIngestResult{}, err
	}
	var owner model.AgentRuntime
	ownerFound, err := database.ReadProjection(RuntimeOwnershipProjection, event.WorkItemID, &owner)
	if err != nil {
		return RuntimeIngestResult{}, err
	}
	if !ownerFound || owner.ID != event.RuntimeID {
		return RuntimeIngestResult{}, &RuntimeOwnerMismatchError{WorkItemID: event.WorkItemID, EventRuntimeID: event.RuntimeID, CurrentRuntimeID: owner.ID}
	}

	activity, _, err := loadRuntimeActivity(database, event.WorkItemID)
	if err != nil {
		return RuntimeIngestResult{}, err
	}
	header := runtimeEventHeader{ProtocolVersion: ProtocolVersion, RuntimeID: event.RuntimeID, WorkItemID: event.WorkItemID, Type: event.Type, Timestamp: event.Timestamp.UTC(), RequestID: compactIdentifier(event.RequestID, 200), Role: compactIdentifier(event.Role, 32), ToolName: compactIdentifier(event.ToolName, 128)}
	applied := applyRuntimeActivity(&activity, header, event.WorkItemID, event.RuntimeID)
	updates := []ProjectionUpdate{}
	if applied {
		activity.ObservedAt = time.Now().UTC()
		activity.Source = "daemon.runtime.live"
		activityBytes, _ := json.Marshal(activity)
		updates = append(updates, ProjectionUpdate{Projection: RuntimeActivityProjection, Key: event.WorkItemID, Value: activityBytes})

		observation := AgentObservation{WorkItemID: event.WorkItemID, Activity: AttentionActivity{WorkItemID: event.WorkItemID}, Warnings: []string{}}
		_, _ = database.ReadProjection(AgentObservationProjection, event.WorkItemID, &observation)
		applyLiveRuntimeObservation(&observation, event)
		observation.ObservedAt = activity.ObservedAt
		observationBytes, _ := json.Marshal(observation)
		updates = append(updates, ProjectionUpdate{Projection: AgentObservationProjection, Key: event.WorkItemID, Value: observationBytes})
	}
	payload, _ := json.Marshal(map[string]any{"runtime_id": event.RuntimeID, "request_id": header.RequestID, "role": header.Role, "tool_name": header.ToolName, "failed": event.Failed})
	commandID := "runtime-event:" + event.ID
	committed, err := database.Commit(Command{ID: commandID, ProtocolVersion: ProtocolVersion, Type: "runtime.event.ingest", Actor: "runtime", ItemID: event.WorkItemID, ReceivedAt: time.Now().UTC()}, []PendingEvent{{ID: event.ID, ItemID: event.WorkItemID, Type: event.Type, Timestamp: event.Timestamp.UTC(), Actor: "runtime", Payload: payload}}, updates)
	if err != nil {
		return RuntimeIngestResult{}, err
	}
	sequence := committed.FinalSequence
	if len(committed.Events) > 0 {
		sequence = committed.Events[0].Sequence
	}
	return RuntimeIngestResult{EventID: event.ID, Sequence: sequence, ItemRevision: committed.ItemRevision, Duplicate: committed.Duplicate}, nil
}

func allowedLiveRuntimeEvent(eventType string) bool {
	switch eventType {
	case agent.EventRuntimeReady, agent.EventRuntimeStopping, agent.EventRuntimeFailed,
		agent.EventAgentStarted, agent.EventAgentSettled,
		agent.EventTurnStarted, agent.EventTurnEnded,
		agent.EventMessageCompleted,
		agent.EventToolStarted, agent.EventToolCompleted,
		agent.EventCommandAccepted, agent.EventCommandRejected:
		return true
	default:
		return false
	}
}

func applyLiveRuntimeObservation(observation *AgentObservation, event RuntimeSemanticEvent) {
	at := event.Timestamp.UTC()
	observation.LastActivityAt = &at
	switch event.Type {
	case agent.EventTurnStarted, agent.EventAgentStarted, agent.EventCommandAccepted, agent.EventToolStarted:
		observation.Status = "busy"
		observation.Reason = "live runtime event reports active work"
		observation.TurnState = "incomplete"
		observation.Activity.LastRequestedAt = &at
	case agent.EventTurnEnded, agent.EventAgentSettled:
		observation.Status = "idle"
		observation.Reason = "live runtime event reports settled work"
		observation.TurnState = "idle"
		observation.Activity.LastCompletedAt = &at
	case agent.EventRuntimeFailed, agent.EventCommandRejected:
		observation.Status = "problem"
		observation.Reason = "live runtime event reports a failure"
	case agent.EventRuntimeReady:
		observation.ProcessOnline = true
		observation.Status = "idle"
		observation.Reason = "runtime is ready"
		observation.TurnState = "idle"
	}
}
