package main

import v1 "github.com/regb/workitem/gen/wi/core/v1"

// messageText is a message's text parts, joined as a reader sees them.
func messageText(m *v1.ConversationMessage) string {
	text := ""
	for _, part := range m.GetParts() {
		if t, ok := part.Kind.(*v1.Part_Text); ok {
			if text != "" {
				text += "\n\n"
			}
			text += t.Text
		}
	}
	return text
}

// messageTools are a message's tool calls, in order.
func messageTools(m *v1.ConversationMessage) []*v1.ToolCall {
	tools := []*v1.ToolCall{}
	for _, part := range m.GetParts() {
		if tool := part.GetTool(); tool != nil {
			tools = append(tools, tool)
		}
	}
	return tools
}
