package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/model"
)

func safeRuntimeID(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, `/\\`) && len(value) <= 200
}

type RuntimeSemanticEvent struct {
	ID            string    `json:"id"`
	RuntimeID     string    `json:"runtime_id"`
	WorkItemID    string    `json:"work_item_id"`
	Type          string    `json:"type"`
	Timestamp     time.Time `json:"timestamp"`
	CommandID     string    `json:"command_id,omitempty"`
	RequestID     string    `json:"request_id,omitempty"`
	Role          string    `json:"role,omitempty"`
	ToolName      string    `json:"tool_name,omitempty"`
	Failed        bool      `json:"failed,omitempty"`
	Readiness     string    `json:"readiness,omitempty"`
	ReasonCode    string    `json:"reason_code,omitempty"`
	DeliveryState string    `json:"delivery_state,omitempty"`
	Outcome       string    `json:"outcome,omitempty"`
	// AgentState is a native current-state observation, not inferred settlement.
	AgentState string `json:"agent_state,omitempty"`
	LeafID     string `json:"leaf_id,omitempty"`
}

// RuntimeEventRejection is a definitive validation or workspace-fence error,
// not a failure to persist an otherwise valid event.
type RuntimeEventRejection struct{ Reason string }

func (e *RuntimeEventRejection) Error() string { return e.Reason }

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
		return RuntimeIngestResult{}, &RuntimeEventRejection{Reason: "valid event, runtime, and work-item ids are required"}
	}
	if event.Timestamp.IsZero() {
		return RuntimeIngestResult{}, &RuntimeEventRejection{Reason: "runtime event timestamp is required"}
	}
	if !allowedLiveRuntimeEvent(event.Type) {
		return RuntimeIngestResult{}, &RuntimeEventRejection{Reason: fmt.Sprintf("runtime event type %q is not retained", event.Type)}
	}
	if !driverprotocol.ValidReadiness(event.Readiness) || !driverprotocol.ValidReasonCode(event.ReasonCode) || !driverprotocol.ValidDeliveryState(event.DeliveryState) {
		return RuntimeIngestResult{}, &RuntimeEventRejection{Reason: "invalid compact runtime status facts"}
	}
	if event.Type == agent.EventRuntimeStatus && event.Readiness == "" {
		return RuntimeIngestResult{}, &RuntimeEventRejection{Reason: "runtime.status requires readiness"}
	}
	if event.Outcome != "" && (event.Type != agent.EventAgentSettled || (event.Outcome != "completed" && event.Outcome != "failed") || (event.Outcome == "completed" && event.Failed)) {
		return RuntimeIngestResult{}, &RuntimeEventRejection{Reason: "invalid agent settlement outcome"}
	}
	if !driverprotocol.ValidAgentObservation(event.AgentState, event.LeafID, event.Readiness) || ((event.AgentState != "" || event.LeafID != "") && event.Type != agent.EventRuntimeStatus) {
		return RuntimeIngestResult{}, &RuntimeEventRejection{Reason: "invalid native agent state observation"}
	}
	canonical, err := database.CanonicalManifest(event.WorkItemID)
	if err != nil {
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

	if owner.WorkspaceClaimID != "" {
		claim := canonical.Manifest.Checkout.Claim
		if claim == nil || claim.ID != owner.WorkspaceClaimID || claim.Generation != owner.WorkspaceClaimGeneration {
			return RuntimeIngestResult{}, &RuntimeEventRejection{Reason: fmt.Sprintf("runtime %s belongs to a superseded workspace claim", owner.ID)}
		}
	}

	activity, _, err := loadRuntimeActivity(database, event.WorkItemID)
	if err != nil {
		return RuntimeIngestResult{}, err
	}
	header := runtimeEventHeader{ProtocolVersion: ProtocolVersion, RuntimeID: event.RuntimeID, WorkItemID: event.WorkItemID, Type: event.Type, Timestamp: event.Timestamp.UTC(), RequestID: compactIdentifier(event.RequestID, 200), Role: compactIdentifier(event.Role, 32), ToolName: compactIdentifier(event.ToolName, 128), Failed: event.Failed, Outcome: event.Outcome, Readiness: event.Readiness, AgentState: event.AgentState, LeafID: event.LeafID, ReasonCode: compactIdentifier(event.ReasonCode, 64)}
	// A setting change or an accepted abort is recorded for its requester but
	// is not agent activity.
	applied := !driverprotocol.QuietOutcome(event.ReasonCode) && applyRuntimeActivity(&activity, header, event.WorkItemID, event.RuntimeID)
	updates := []ProjectionUpdate{}
	readiness := event.Readiness
	if event.Type == agent.EventRuntimeReady && readiness == "" {
		readiness = "ready"
	}
	if readiness != "" && applied && (owner.ReadinessObservedAt == nil || !event.Timestamp.Before(*owner.ReadinessObservedAt)) {
		owner.Readiness = readiness
		owner.ReadinessReasonCode = event.ReasonCode
		at := event.Timestamp.UTC()
		owner.ReadinessObservedAt = &at
		value, _ := json.Marshal(owner)
		updates = append(updates, ProjectionUpdate{Projection: RuntimeOwnershipProjection, Key: event.WorkItemID, Value: value})
		if event.Type == agent.EventRuntimeStatus {
			applied = true
		}
	}
	if applied {
		activity.ObservedAt = time.Now().UTC()
		activity.Source = "daemon.runtime.live"
		activityBytes, _ := json.Marshal(activity)
		updates = append(updates, ProjectionUpdate{Projection: RuntimeActivityProjection, Key: event.WorkItemID, Value: activityBytes})

		observation := AgentObservation{WorkItemID: event.WorkItemID, Activity: AttentionActivity{WorkItemID: event.WorkItemID}, Warnings: []string{}}
		_, _ = database.ReadProjection(AgentObservationProjection, event.WorkItemID, &observation)
		applyLiveRuntimeObservation(&observation, event, activity)
		observation.ObservedAt = activity.ObservedAt
		observationBytes, _ := json.Marshal(observation)
		updates = append(updates, ProjectionUpdate{Projection: AgentObservationProjection, Key: event.WorkItemID, Value: observationBytes})
	}
	payload, _ := json.Marshal(map[string]any{"runtime_id": event.RuntimeID, "command_id": compactIdentifier(event.CommandID, 200), "request_id": header.RequestID, "role": header.Role, "tool_name": header.ToolName, "failed": event.Failed, "outcome": event.Outcome, "agent_state": event.AgentState, "leaf_id": event.LeafID, "readiness": event.Readiness, "reason_code": event.ReasonCode, "delivery_state": event.DeliveryState})
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
	case agent.EventRuntimeStatus, agent.EventQueueChanged, agent.EventRuntimeReady, agent.EventRuntimeStopping, agent.EventRuntimeFailed,
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

func applyLiveRuntimeObservation(observation *AgentObservation, event RuntimeSemanticEvent, activity RuntimeActivity) {
	at := event.Timestamp.UTC()
	observation.AgentLoopState = activity.AgentState
	observation.LastActivityAt = &at
	switch event.Type {
	case agent.EventTurnStarted, agent.EventAgentStarted, agent.EventCommandAccepted, agent.EventToolStarted:
		observation.Status = "busy"
		observation.Reason = "live runtime event reports active work"
		if event.Type == agent.EventCommandAccepted && event.ReasonCode == "native_confirmed" {
			observation.Reason = "Native command receipt confirmed; execution not yet observed"
		}
		observation.TurnState = "incomplete"
		observation.Activity.LastRequestedAt = &at
	case agent.EventTurnEnded, agent.EventAgentSettled:
		if (event.Type == agent.EventTurnEnded && activity.AgentState != "active" && !event.Failed) || (event.Type == agent.EventAgentSettled && activity.AgentState == "") {
			break
		}
		if activity.AgentState == "active" {
			observation.Status = "busy"
			observation.Reason = "live agent loop is active"
			observation.TurnState = "incomplete"
			break
		}
		if activity.AgentState == "failed" {
			observation.Status = "problem"
			observation.Reason = "live agent loop settled with a failure"
			if reason := driverprotocol.RuntimeReasonText(activity.FailureReason); reason != "" {
				observation.Reason = reason
			}
			observation.TurnState = "failed"
			observation.Activity.LastCompletedAt = &at
			break
		}
		observation.Status = "idle"
		observation.Reason = "live runtime event reports settled work"
		observation.TurnState = "idle"
		if event.Failed {
			observation.Status = "problem"
			observation.Reason = "live runtime event reports failed or interrupted work"
			if reason := driverprotocol.RuntimeReasonText(event.ReasonCode); reason != "" {
				observation.Reason = reason
			}
			observation.TurnState = "failed"
		}
		observation.Activity.LastCompletedAt = &at
	case agent.EventRuntimeFailed, agent.EventCommandRejected:
		observation.Status = "problem"
		observation.Reason = "live runtime event reports a failure"
		if reason := driverprotocol.RuntimeReasonText(event.ReasonCode); reason != "" {
			observation.Reason = reason
		}
	case agent.EventRuntimeStatus:
		observation.ProcessOnline = true
		if activity.RuntimeState == model.AgentRuntimeProblem {
			observation.Status = "problem"
			observation.TurnState = "failed"
			observation.Reason = "live runtime reports failure"
			break
		}
		if event.AgentState != "" {
			switch event.AgentState {
			case "active":
				observation.Status = "busy"
				observation.TurnState = "incomplete"
				observation.Reason = "native agent reports active work"
			case "idle":
				observation.Status = "idle"
				observation.TurnState = "idle"
				observation.Reason = "native agent reports idle on its selected branch"
			case "failed":
				observation.Status = "problem"
				observation.TurnState = "failed"
				observation.Reason = "selected native branch ends with a failure"
			}
			break
		}
		// Input readiness is independent of the last turn's outcome. Returning
		// to an empty editor or JSON inbox must not erase actionable failure.
		if event.Readiness == "ready" && observation.TurnState == "failed" {
			return
		}
		observation.Reason = driverprotocol.RuntimeReasonText(event.ReasonCode)
		switch {
		case inputBlocked(event.Readiness, event.ReasonCode):
			observation.Status = "problem"
		case event.Readiness == "waiting_input":
			// A draft or another TUI screen: someone is at the terminal. A
			// question asked mid-turn waits for the operator, not for a fix.
			observation.Status = "idle"
		}
		if promptReceived(event.Readiness, event.ReasonCode) {
			// The agent has the prompt: from the operator's side, it is working.
			observation.Status = "busy"
			observation.TurnState = "incomplete"
			return
		}
		switch event.Readiness {
		case "starting", "stopping", "awaiting_confirmation":
			observation.Status = "idle"
		case "busy":
			observation.Status = "busy"
			observation.TurnState = "incomplete"
		case "ready":
			observation.Status = "idle"
		}
	case agent.EventRuntimeReady:
		observation.ProcessOnline = true
		if observation.TurnState == "failed" && event.Readiness != "busy" {
			return
		}
		observation.Status = "idle"
		observation.Reason = "runtime is ready"
		observation.TurnState = "idle"
		if event.Readiness == "busy" {
			observation.Status = "busy"
			observation.TurnState = "incomplete"
			observation.Reason = driverprotocol.RuntimeReasonText(event.ReasonCode)
		}
	}
	if activity.AgentState == "active" && activity.RuntimeState != model.AgentRuntimeProblem && !(event.Type == agent.EventRuntimeStatus && (inputBlocked(event.Readiness, event.ReasonCode) || awaitingAnswer(event.Readiness, event.ReasonCode))) {
		observation.Status = "busy"
		observation.Reason = "live agent loop is active"
		observation.TurnState = "incomplete"
	}
}

// inputBlocked reports whether a runtime's input readiness needs the
// operator. A draft or queued message in a TUI editor, or another screen open
// over it, means someone is using the terminal, not that the agent is stuck.
func inputBlocked(readiness, reason string) bool {
	switch readiness {
	case "blocked":
		return true
	case "waiting_input":
		return reason != "editor_not_empty" && reason != "editor_not_visible" && !awaitingAnswer(readiness, reason)
	}
	return false
}

// promptReceived reports an agent that has received a prompt but not yet
// shown work on it: the model is thinking. It counts as working.
func promptReceived(readiness, reason string) bool {
	return readiness == "awaiting_confirmation" && reason == "execution_pending"
}

// awaitingAnswer reports a turn paused on a question, plan approval or
// permission prompt. The turn is still open, but only the operator can move
// it on, so it needs attention rather than showing as work in progress.
func awaitingAnswer(readiness, reason string) bool {
	return readiness == "waiting_input" && driverprotocol.AwaitsAnswer(reason)
}
