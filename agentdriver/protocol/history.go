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
	NativeIDs []string       `json:"native_ids,omitempty"`
	ID        string         `json:"id"`
	Role      string         `json:"role"`
	Text      string         `json:"text"`
	Timestamp string         `json:"timestamp,omitempty"`
	Tools     []HistoryTool  `json:"tools,omitempty"`
	Images    []HistoryImage `json:"images,omitempty"`
	// Error marks a reply the agent's harness wrote in place of the model's,
	// such as a usage limit notice; clients show it as a notice.
	Error bool `json:"error,omitempty"`
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
}

type HistoryImage struct {
	MIMEType string `json:"mime_type"`
	Data     string `json:"data"`
	Alt      string `json:"alt,omitempty"`
}
