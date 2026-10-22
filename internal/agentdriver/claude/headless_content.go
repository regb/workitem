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
	tools          map[string]*protocol.ContentTool
	toolOrder      []string
	server         *protocol.ContentServer
	emit           func(protocol.RuntimeEvent)
	text, activity string
	truncated      bool
	tool           *protocol.ContentTool
	blocks         map[int]*headlessBlock
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
				s.tool = &protocol.ContentTool{ID: b.id, Name: b.name, Status: "running"}
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
					s.tool = &protocol.ContentTool{ID: b.id, Name: b.name, Input: b.input, Status: "running"}
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
		saved := ""
		if f.Type == "assistant" && message.Text != "" {
			s.text, s.truncated = boundedDisplay(message.Text, maxDisplayText)
			s.truncated = s.truncated || truncated
			s.activity = "Assistant message completed"
			// The assistant frame is the transcript record holding this text.
			saved = message.Text
		}
		for _, t := range message.Tools {
			if len(t.ID) > 200 || len(t.Name) > 128 {
				return fmt.Errorf("invalid Claude tool metadata")
			}
			if t.Status == "running" {
				// Partial frames normally already emitted tool.started. The complete
				// assistant record fills its input preview rather than duplicating it.
				seen := s.tools[t.ID] != nil && s.tools[t.ID].Status == "running"
				s.tool = &protocol.ContentTool{ID: t.ID, Name: t.Name, Input: t.Input, Status: "running"}
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
				s.tool = &protocol.ContentTool{ID: t.ID, Name: name, Input: input, Output: t.Output, Status: t.Status}
				s.emit(protocol.RuntimeEvent{Type: protocol.EventToolCompleted, ToolName: name, Failed: t.Status == "failed"})
			}
			s.rememberTool(s.tool)
			s.activity = "Tool " + s.tool.Status
		}
		s.publish()
		if saved != "" && uuidPattern.MatchString(f.UUID) {
			s.server.MarkSaved(saved, []string{f.UUID})
		}
	}
	return nil
}
func (s *headlessContent) rememberTool(tool *protocol.ContentTool) {
	if s.tools == nil {
		s.tools = map[string]*protocol.ContentTool{}
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
