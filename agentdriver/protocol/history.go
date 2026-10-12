package driverprotocol

// History is a bounded chronological view of one branch. LeafID is the native
// branch identity. Truncated means earlier entries or message text were omitted.
// History is read-only native content, never a compact daemon event.
type History struct {
	Messages  []HistoryMessage `json:"messages"`
	LeafID    string           `json:"leaf_id"`
	Truncated bool             `json:"truncated"`
	Warnings  []string         `json:"warnings,omitempty"`
}

type HistoryMessage struct {
	ID        string         `json:"id"`
	Role      string         `json:"role"`
	Text      string         `json:"text"`
	Timestamp string         `json:"timestamp,omitempty"`
	Tools     []HistoryTool  `json:"tools,omitempty"`
	Images    []HistoryImage `json:"images,omitempty"`
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
