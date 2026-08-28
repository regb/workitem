package coordinator

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/model"
)

const (
	runtimeBatchRecords = 1000
	runtimeBatchBytes   = 4 << 20
)

type runtimeEventHeader struct {
	ProtocolVersion  int       `json:"protocol_version"`
	EventID          string    `json:"event_id,omitempty"`
	RuntimeID        string    `json:"runtime_id"`
	WorkItemID       string    `json:"work_item_id"`
	Type             string    `json:"type"`
	Timestamp        time.Time `json:"timestamp"`
	CommandID        string    `json:"command_id,omitempty"`
	RequestID        string    `json:"request_id,omitempty"`
	Role             string    `json:"role,omitempty"`
	ToolCallID       string    `json:"tool_call_id,omitempty"`
	ToolName         string    `json:"tool_name,omitempty"`
	Backend          string    `json:"backend,omitempty"`
	BackendEventType string    `json:"backend_event_type,omitempty"`
}

func fileFingerprint(file *os.File, size int64) (string, error) {
	if size < 0 {
		return "", errors.New("invalid fingerprint size")
	}
	data := make([]byte, size)
	if size > 0 {
		if _, err := file.ReadAt(data, 0); err != nil && err != io.EOF {
			return "", err
		}
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func readBoundedJSONLine(reader *bufio.Reader, limit int) (line []byte, consumed int64, complete, oversized bool, resultErr error) {
	for {
		fragment, err := reader.ReadSlice('\n')
		consumed += int64(len(fragment))
		if !oversized {
			if len(line)+len(fragment) > limit {
				line = nil
				oversized = true
			} else {
				line = append(line, fragment...)
			}
		}
		switch {
		case err == nil:
			return bytes.TrimSpace(line), consumed, true, oversized, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return nil, 0, false, oversized, nil
		default:
			return nil, consumed, false, oversized, err
		}
	}
}

func loadRuntimeActivity(database *Database, itemID string) (RuntimeActivity, bool, error) {
	activity := RuntimeActivity{WorkItemID: itemID}
	found, err := database.ReadProjection(RuntimeActivityProjection, itemID, &activity)
	return activity, found, err
}

func applyRuntimeActivity(activity *RuntimeActivity, event runtimeEventHeader, itemID, runtimeID string) bool {
	if activity.LastEventAt != nil && event.Timestamp.Before(*activity.LastEventAt) {
		return false
	}
	if activity.RuntimeID != "" && activity.RuntimeID != runtimeID {
		*activity = RuntimeActivity{WorkItemID: itemID}
	}
	at := event.Timestamp.UTC()
	activity.WorkItemID = itemID
	activity.RuntimeID = runtimeID
	activity.LastEventAt = &at
	switch event.Type {
	case agent.EventRuntimeReady:
		activity.RuntimeState = model.AgentRuntimeRunning
		activity.TurnState = "idle"
	case agent.EventAgentStarted:
		activity.RuntimeState = model.AgentRuntimeRunning
		activity.TurnState = "busy"
		activity.LastRequestedAt = &at
	case agent.EventRuntimeStopping:
		activity.RuntimeState = model.AgentRuntimeStopping
	case agent.EventRuntimeFailed:
		activity.RuntimeState = model.AgentRuntimeProblem
	case agent.EventTurnStarted, agent.EventCommandAccepted, agent.EventToolStarted:
		activity.TurnState = "busy"
		activity.LastRequestedAt = &at
	case agent.EventTurnEnded, agent.EventAgentSettled:
		activity.TurnState = "idle"
		activity.LastCompletedAt = &at
	case agent.EventMessageCompleted:
		if event.Role == "assistant" || event.Role == "" {
			activity.LastCompletedAt = &at
		}
	}
	return true
}
