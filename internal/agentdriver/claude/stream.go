package claude

import (
	"context"
	"encoding/json"
	"fmt"
	protocol "github.com/regb/workitem/agentdriver/protocol"
	"strings"
	"time"
	"unicode/utf8"
)

const maxDisplayDelta = 16 << 10
const maxDisplayText = 256 << 10

type displayFields struct {
	TurnID    string `json:"turn_id"`
	MessageID string `json:"message_id"`
	Index     int    `json:"index"`
	Final     bool   `json:"final"`
	Delta     string `json:"delta"`
	Truncated bool   `json:"truncated,omitempty"`
}
type displayInput struct {
	hookInput
	displayFields
}

func displaySocketPath(hookPath string) string {
	return privateSocketPath(hookPath, "claude-display")
}
func boundedDisplay(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	text = text[:limit]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text, true
}

// Display text goes only to a private runtime-local endpoint, never semantic
// hooks or compact events. Optional streaming failures must not delay the TUI.
func submitDisplay(ctx context.Context, path string, h hookInput, fields displayFields) error {
	fields.Delta, fields.Truncated = boundedDisplay(fields.Delta, maxDisplayDelta)
	payload, _ := json.Marshal(displayInput{hookInput: h, displayFields: fields})
	callCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	_ = protocol.SubmitControl(callCtx, displaySocketPath(path), protocol.ControlCommand{ProtocolVersion: protocol.RuntimeVersion, ID: fmt.Sprintf("display-%d", time.Now().UnixNano()), Type: "display", Message: string(payload)})
	return nil
}

type displayStream struct {
	firstAt, savedAt                     time.Time
	savedPromptID                        string
	server                               *protocol.ContentServer
	turns                                *turnTracker
	promptID, messageID, turnID, text    string
	next                                 int
	active, final, incomplete, truncated bool
	retired                              map[string]bool
	retiredOrder                         []string
	leaf, savedText, activity            string
	tool                                 *protocol.ContentTool
}

func (s *displayStream) reset() {
	s.active = false
	s.final = false
	s.text = ""
	s.messageID = ""
	s.promptID = ""
	s.turnID = ""
	s.next = 0
	s.incomplete = false
	s.truncated = false
}
func (s *displayStream) accept(h displayInput) (bool, error) {
	if h.Index < 0 || h.Index > 1000000 || h.MessageID == "" || len(h.MessageID) > 128 || h.TurnID == "" || len(h.TurnID) > 128 || len(h.Delta) > maxDisplayDelta || !utf8.ValidString(h.Delta) {
		return false, fmt.Errorf("invalid display metadata")
	}
	if !s.turns.matches(h.PromptID, h.ObservedAt) || s.retired[h.MessageID] {
		return false, nil
	}
	if h.MessageID != s.messageID {
		if s.messageID != "" {
			if s.retired == nil {
				s.retired = map[string]bool{}
			}
			s.retired[s.messageID] = true
			s.retiredOrder = append(s.retiredOrder, s.messageID)
			if len(s.retiredOrder) > 1024 {
				delete(s.retired, s.retiredOrder[0])
				s.retiredOrder = s.retiredOrder[1:]
			}
		}
		s.reset()
		s.active = true
		s.promptID = h.PromptID
		s.turnID = h.TurnID
		s.messageID = h.MessageID
		s.firstAt = h.ObservedAt
	}
	if h.Index < s.next || s.final {
		return false, nil
	}
	if h.Index != s.next {
		s.incomplete = true
	}
	s.next = h.Index + 1
	s.final = h.Final
	s.truncated = s.truncated || h.Truncated
	if !s.incomplete {
		var truncated bool
		s.text, truncated = boundedDisplay(s.text+h.Delta, maxDisplayText)
		s.truncated = s.truncated || truncated
	}
	if h.Truncated {
		s.incomplete = true
	}
	s.publish()
	return true, nil
}
func (s *displayStream) publish() {
	if !s.active {
		s.server.PublishTranscript(s.leaf, s.savedText, s.activity, s.tool)
		return
	}
	activity := "Streaming assistant lines"
	if s.final {
		activity = "Assistant message displayed; awaiting transcript"
	}
	if s.incomplete {
		activity = "Streaming batches missing; awaiting transcript"
	}
	if s.truncated {
		activity += " (truncated)"
	}
	s.server.PublishStreaming(s.leaf, s.text, activity, s.tool, s.truncated || s.incomplete)
}
func (s *displayStream) PublishPromptTranscript(leaf, text, activity string, tool *protocol.ContentTool, promptID string, at time.Time) {
	s.savedPromptID = promptID
	s.savedAt = at
	s.PublishTranscript(leaf, text, activity, tool)
}
func (s *displayStream) PublishTranscript(leaf, text, activity string, tool *protocol.ContentTool) {
	s.leaf, s.savedText, s.activity, s.tool = leaf, text, activity, tool
	// Native history remains authoritative. Do not replace an in-flight display
	// with the previous record while native transcript appends lag behind hooks.
	// Claude can commit the final record before an optional display hook starts.
	// After native settlement, a complete record for this prompt is authoritative
	// even if its timestamp precedes the first observed display batch.
	settledRecord := s.turns.settled && !s.turns.started.IsZero() && !s.savedAt.IsZero() && !s.savedAt.Before(s.turns.started.Truncate(time.Millisecond))
	sameText := !s.incomplete && !s.truncated && strings.TrimSpace(text) == strings.TrimSpace(s.text)
	currentRecord := activity == "Message saved" && s.promptID != "" && s.savedPromptID == s.promptID
	afterDisplay := !s.savedAt.IsZero() && !s.savedAt.Before(s.firstAt.Truncate(time.Millisecond))
	matchingPrefix := s.text != "" && strings.HasPrefix(text, s.text)
	if s.active && s.final && (sameText || (s.incomplete || s.truncated) && currentRecord && (settledRecord || afterDisplay || matchingPrefix)) {
		s.active = false
	}
	s.publish()
}

// MarkSaved passes a saved record to the content server, which links it only
// while the shown text is that record's text.
func (s *displayStream) MarkSaved(text string, nativeIDs []string) {
	s.server.MarkSaved(text, nativeIDs)
}
