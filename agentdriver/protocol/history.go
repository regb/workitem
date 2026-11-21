package driverprotocol

// History is a bounded chronological view of one branch. LeafID is the native
// branch identity. Truncated means older entries or oversized content were
// omitted from this page. HasMore and BeforeID allow paging earlier entries.
// History is read-only native content, never a compact daemon event.
type History struct {
	// NativeEntryIDs preserves native chronology even when tool results fold into calls.
	NativeEntryIDs []string         `json:"native_entry_ids,omitempty"`
	HasMore        bool             `json:"has_more,omitempty"`
	BeforeID       string           `json:"before_id,omitempty"`
	Messages       []HistoryMessage `json:"messages"`
	LeafID         string           `json:"leaf_id"`
	Truncated      bool             `json:"truncated"`
	Warnings       []string         `json:"warnings,omitempty"`
}

type HistoryMessage struct {
	NativeIDs []string `json:"native_ids,omitempty"`
	ID        string   `json:"id"`
	Role      string   `json:"role"`
	Timestamp string   `json:"timestamp,omitempty"`
	// Parts are the message in the order it was written: its text, the
	// model's reasoning, its tool calls and images, as live content shows
	// them while it is written.
	Parts []Part `json:"parts"`
	// Error marks a reply the agent's harness wrote in place of the model's,
	// such as a usage limit notice; clients show it as a notice.
	Error bool `json:"error,omitempty"`
	// UsageProvider names whose quota ran out, for an Error that reports a
	// usage limit, as the usage plugin names providers (claude, codex).
	UsageProvider string `json:"usage_provider,omitempty"`
	// Aborted marks a reply the operator stopped before it finished; the
	// turn ended there, and the agent takes the next message as usual.
	Aborted bool `json:"aborted,omitempty"`
	// Subagent marks a message reporting a subagent's result, with role
	// "subagent"; its text is the result the subagent returned.
	Subagent *HistorySubagent `json:"subagent,omitempty"`
	// Summary describes a message with role "summary": what it summarizes
	// and, for a compaction, how large the context was before it.
	Summary *HistorySummary `json:"summary,omitempty"`
	// ClientMessageID is the request ID of the prompt this message is, set
	// by the host from the links drivers report (prompt.saved), never by a
	// driver.
	ClientMessageID string `json:"client_message_id,omitempty"`
}

// Part is one stretch of a message, saved or live: exactly one of text, the
// model's reasoning, a tool call or an image.
type Part struct {
	Text    string        `json:"text,omitempty"`
	Thought *Thought      `json:"thought,omitempty"`
	Tool    *HistoryTool  `json:"tool,omitempty"`
	Image   *HistoryImage `json:"image,omitempty"`
}

// Thought is the model's reasoning: its text when the agent shares it (Pi
// does; Claude saves only that it reasoned), and how long it took when
// known.
type Thought struct {
	Text       string `json:"text,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// TextPart, ThoughtPart, ToolPart and ImagePart build one part.
func TextPart(text string) Part         { return Part{Text: text} }
func ThoughtPart(thought Thought) Part  { return Part{Thought: &thought} }
func ToolPart(tool HistoryTool) Part    { return Part{Tool: &tool} }
func ImagePart(image HistoryImage) Part { return Part{Image: &image} }

// IsText reports a text part.
func (p Part) IsText() bool { return p.Thought == nil && p.Tool == nil && p.Image == nil }

// Text is the message's text parts, joined with a blank line where other
// parts came between.
func (m HistoryMessage) Text() string { return PartsText(m.Parts) }

// Tools are the message's tool calls, in order.
func (m HistoryMessage) Tools() []HistoryTool { return PartsTools(m.Parts) }

// Images are the message's images, in order.
func (m HistoryMessage) Images() []HistoryImage {
	out := []HistoryImage{}
	for _, part := range m.Parts {
		if part.Image != nil {
			out = append(out, *part.Image)
		}
	}
	return out
}

// PartsText joins text parts with a blank line where other parts came
// between.
func PartsText(parts []Part) string {
	text := ""
	for _, part := range parts {
		if part.IsText() && part.Text != "" {
			if text != "" {
				text += "\n\n"
			}
			text += part.Text
		}
	}
	return text
}

// PartsTools are the tool calls among parts, in order.
func PartsTools(parts []Part) []HistoryTool {
	out := []HistoryTool{}
	for _, part := range parts {
		if part.Tool != nil {
			out = append(out, *part.Tool)
		}
	}
	return out
}

// AppendText adds text to its last part when that is text, else as a new
// part. Empty text adds nothing.
func AppendText(parts []Part, text string) []Part {
	if text == "" {
		return parts
	}
	if n := len(parts); n > 0 && parts[n-1].IsText() {
		next := append([]Part(nil), parts...)
		next[n-1].Text += text
		return next
	}
	return append(append([]Part(nil), parts...), TextPart(text))
}

// UpsertTool replaces the part holding the call with the same ID (without
// an ID, the last call of the same name without one), in place, or adds the
// call as a new part.
func UpsertTool(parts []Part, tool HistoryTool) []Part {
	next := append([]Part(nil), parts...)
	for i := len(next) - 1; i >= 0; i-- {
		if t := next[i].Tool; t != nil && (tool.ID != "" && t.ID == tool.ID || tool.ID == "" && t.ID == "" && t.Name == tool.Name) {
			next[i] = ToolPart(tool)
			return next
		}
	}
	return append(next, ToolPart(tool))
}

// HistorySummary is what kind of summary a "summary" message is: a
// "compaction", which replaced the conversation before it in the agent's
// context, or a "branch" summary of a branch the conversation left.
type HistorySummary struct {
	Kind         string `json:"kind"`
	TokensBefore int    `json:"tokens_before,omitempty"`
}

// HistoryTool joins a native tool call and its eventual result so clients can
// render one disclosure that changes from running to completed.
type HistoryTool struct {
	ID     string         `json:"id,omitempty"`
	Name   string         `json:"name"`
	Input  string         `json:"input,omitempty"`
	Output string         `json:"output,omitempty"`
	Status string         `json:"status"`
	Images []HistoryImage `json:"images,omitempty"`
	// Subagent is the agent this call delegated to, whose own conversation a
	// driver with the subagents capability reads by its ID.
	Subagent *HistorySubagent `json:"subagent,omitempty"`
}

// HistorySubagent identifies a subagent and what is known of its run.
// Status is running, completed or failed.
type HistorySubagent struct {
	ID          string `json:"id"`
	ToolID      string `json:"tool_id,omitempty"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"`
}

type HistoryImage struct {
	MIMEType string `json:"mime_type"`
	Data     string `json:"data"`
	Alt      string `json:"alt,omitempty"`
}
