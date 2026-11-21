package claude

import (
	"encoding/json"
	"fmt"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

type headlessBlock struct {
	id, name, input string
	tool            bool
}
type headlessContent struct {
	tools          map[string]*protocol.HistoryTool
	toolOrder      []string
	server         *protocol.ContentServer
	emit           func(protocol.RuntimeEvent)
	text, activity string
	truncated      bool
	tool           *protocol.HistoryTool
	blocks         map[int]*headlessBlock
	// savedText and savedIDs are the current message's text and the
	// records holding it: Claude saves each block of a reply as its own.
	savedText string
	savedIDs  []string
}

func (s *headlessContent) publish() {
	s.server.PublishStreaming("", s.text, s.activity, s.tool, s.truncated)
}
func (s *headlessContent) frame(f headlessFrame) error {
	switch f.Type {
	case "stream_event":
		var e struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
			Block struct {
				Type, ID, Name, Text string
				Input                json.RawMessage
			} `json:"content_block"`
			Delta struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
				Partial  string `json:"partial_json"`
			} `json:"delta"`
		}
		if json.Unmarshal(f.Event, &e) != nil {
			return fmt.Errorf("invalid Claude stream event")
		}
		if e.Index < 0 || e.Index > 255 {
			return fmt.Errorf("Claude content block limit exceeded")
		}
		switch e.Type {
		case "message_start":
			s.text = ""
			s.truncated = false
			s.tool = nil
			s.savedText, s.savedIDs = "", nil
			s.blocks = map[int]*headlessBlock{}
			s.activity = "Assistant responding"
			s.server.StartMessage()
		case "content_block_start":
			if s.blocks == nil {
				s.blocks = map[int]*headlessBlock{}
			}
			if len(e.Block.ID) > 200 || len(e.Block.Name) > 128 {
				return fmt.Errorf("invalid Claude tool metadata")
			}
			b := &headlessBlock{id: e.Block.ID, name: e.Block.Name, tool: e.Block.Type == "tool_use"}
			s.blocks[e.Index] = b
			if b.tool {
				s.tool = &protocol.HistoryTool{ID: b.id, Name: b.name, Status: "running"}
				s.rememberTool(s.tool)
				s.activity = "Tool running: " + b.name
				s.emit(protocol.RuntimeEvent{Type: protocol.EventToolStarted, ToolName: b.name})
			} else if e.Block.Type == "text" {
				s.appendText(e.Block.Text)
			} else if e.Block.Type == "thinking" || e.Block.Type == "redacted_thinking" {
				s.activity = "Thinking"
			}
		case "content_block_delta":
			switch e.Delta.Type {
			case "text_delta":
				s.activity = "Assistant responding"
				s.appendText(e.Delta.Text)
			case "thinking_delta":
				s.activity = "Thinking"
				s.server.Thought(e.Delta.Thinking)
			case "input_json_delta":
				if b := s.blocks[e.Index]; b != nil && b.tool {
					b.input, _ = boundedDisplay(b.input+e.Delta.Partial, 64<<10)
					s.tool = &protocol.HistoryTool{ID: b.id, Name: b.name, Input: b.input, Status: "running"}
					s.rememberTool(s.tool)
				}
			}
		}
		s.publish()
	case "assistant", "user":
		var message protocol.HistoryMessage
		truncated := false
		raw := f.Message.Content
		// Native assistant records can contain thinking-only blocks between text
		// deltas. They must not replace answer text or become tool previews.
		var blocks []json.RawMessage
		if json.Unmarshal(raw, &blocks) == nil {
			filtered := make([]json.RawMessage, 0, len(blocks))
			for _, b := range blocks {
				var tag struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(b, &tag) != nil {
					return fmt.Errorf("invalid Claude message block")
				}
				if tag.Type != "thinking" && tag.Type != "redacted_thinking" {
					filtered = append(filtered, b)
				}
			}
			raw, _ = json.Marshal(filtered)
		}
		content(raw, &message, &truncated)
		saved := false
		if text := message.Text(); f.Type == "assistant" && text != "" {
			// A block's record follows its deltas and corrects the live text
			// only when the deltas showed something else, so text written
			// after a call stays after it.
			whole := s.savedText + text
			if s.text != whole {
				s.text, s.truncated = boundedDisplay(whole, maxDisplayText)
			}
			s.truncated = s.truncated || truncated
			s.activity = "Assistant message completed"
			s.savedText = whole
		}
		// Every assistant frame is a transcript record holding a block of
		// the message, text or not.
		if f.Type == "assistant" && uuidPattern.MatchString(f.UUID) && len(s.savedIDs) < 64 {
			s.savedIDs = append(s.savedIDs, f.UUID)
			saved = true
		}
		for _, t := range message.Tools() {
			if len(t.ID) > 200 || len(t.Name) > 128 {
				return fmt.Errorf("invalid Claude tool metadata")
			}
			if t.Status == "running" {
				// Partial frames normally already emitted tool.started. The complete
				// assistant record fills its input preview rather than duplicating it.
				seen := s.tools[t.ID] != nil && s.tools[t.ID].Status == "running"
				s.tool = &protocol.HistoryTool{ID: t.ID, Name: t.Name, Input: t.Input, Status: "running"}
				if !seen {
					s.emit(protocol.RuntimeEvent{Type: protocol.EventToolStarted, ToolName: t.Name})
				}
			} else {
				name := t.Name
				input := ""
				if prior := s.tools[t.ID]; prior != nil {
					name = prior.Name
					input = prior.Input
				}
				s.tool = &protocol.HistoryTool{ID: t.ID, Name: name, Input: input, Output: t.Output, Status: t.Status}
				s.emit(protocol.RuntimeEvent{Type: protocol.EventToolCompleted, ToolName: name, Failed: t.Status == "failed"})
			}
			s.rememberTool(s.tool)
			s.activity = "Tool " + s.tool.Status
		}
		s.publish()
		if saved {
			s.server.MarkSaved(s.savedText, append([]string(nil), s.savedIDs...))
		}
	}
	return nil
}
func (s *headlessContent) rememberTool(tool *protocol.HistoryTool) {
	if s.tools == nil {
		s.tools = map[string]*protocol.HistoryTool{}
	}
	if s.tools[tool.ID] == nil {
		s.toolOrder = append(s.toolOrder, tool.ID)
	}
	copy := *tool
	s.tools[tool.ID] = &copy
	if len(s.toolOrder) > 128 {
		delete(s.tools, s.toolOrder[0])
		s.toolOrder = s.toolOrder[1:]
	}
}
func (s *headlessContent) appendText(text string) {
	var cut bool
	s.text, cut = boundedDisplay(s.text+text, maxDisplayText)
	s.truncated = s.truncated || cut
}
