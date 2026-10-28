package pi

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
)

type conversationLine struct {
	Type       string `json:"type"`
	Timestamp  string `json:"timestamp"`
	StopReason string `json:"stopReason"`
	Message    struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		StopReason string          `json:"stopReason"`
	} `json:"message"`
}

type contentBlock struct {
	Type string          `json:"type"`
	Text json.RawMessage `json:"text"`
}

func SummarizeConversationFact(line uint64, raw []byte) (driverprotocol.ConversationFact, bool) {
	var value conversationLine
	if json.Unmarshal(raw, &value) != nil {
		return driverprotocol.ConversationFact{}, false
	}
	fact := driverprotocol.ConversationFact{Line: line, Type: compactMetadata(value.Type, 128), Role: compactMetadata(value.Message.Role, 32), StopReason: compactStopReason(firstNonEmpty(value.Message.StopReason, value.StopReason))}
	if value.Timestamp != "" {
		fact.Timestamp, _ = time.Parse(time.RFC3339Nano, value.Timestamp)
	}
	content := bytes.TrimSpace(value.Message.Content)
	hasText, hasTool := false, false
	if len(content) > 0 && !bytes.Equal(content, []byte("null")) {
		if content[0] == '"' {
			hasText = len(content) > 2
			fact.ContentTypes = []string{"text"}
		} else {
			var blocks []contentBlock
			if json.Unmarshal(content, &blocks) != nil {
				return driverprotocol.ConversationFact{}, false
			}
			seen := map[string]bool{}
			for _, block := range blocks {
				block.Type = compactMetadata(block.Type, 64)
				if block.Type != "" && !seen[block.Type] {
					seen[block.Type] = true
					fact.ContentTypes = append(fact.ContentTypes, block.Type)
				}
				hasText = hasText || block.Type == "text" && len(bytes.TrimSpace(block.Text)) > 2
				hasTool = hasTool || block.Type == "toolCall" || block.Type == "bashExecution"
			}
		}
	}
	stop := strings.ToLower(strings.TrimSpace(fact.StopReason))
	fact.Failed = stop == "error" || stop == "errored" || stop == "failed" || stop == "length"
	if fact.Role == "assistant" {
		// An aborted reply ends the turn the operator stopped; it is not a
		// failure, and the next message continues as usual.
		fact.Terminal = fact.Failed || abortedStop(stop) || !hasTool && hasText && (stop == "" || stop == "stop" || stop == "end_turn" || stop == "complete" || stop == "completed")
	}
	return fact, true
}

func compactStopReason(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "stop", "end_turn", "complete", "completed", "error", "errored", "failed", "aborted", "abort", "cancelled", "canceled", "length":
		return value
	default:
		return "other"
	}
}

func compactMetadata(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) > limit {
		return "other"
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			continue
		}
		return "other"
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// abortedStop reports a stop reason for a reply stopped by an abort.
func abortedStop(stop string) bool {
	return stop == "aborted" || stop == "abort" || stop == "cancelled" || stop == "canceled"
}
