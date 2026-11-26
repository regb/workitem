package claude

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

type historyRecord struct {
	Type   string `json:"type"`
	ID     string `json:"uuid"`
	Parent string `json:"parentUuid"`
	// LogicalParent links a compaction boundary, which has no parent, to
	// the conversation it compacted.
	LogicalParent string `json:"logicalParentUuid"`
	PromptID      string `json:"promptId"`
	Timestamp     string `json:"timestamp"`
	Sidechain     bool   `json:"isSidechain"`
	Meta          bool   `json:"isMeta"`
	// Effort is the reasoning effort of an assistant reply.
	Effort         string `json:"effort"`
	CompactSummary bool   `json:"isCompactSummary"`
	APIError       bool   `json:"isApiErrorMessage"`
	// Error classifies an API error reply, such as rate_limit.
	Error       string `json:"error"`
	Subtype     string `json:"subtype"`
	CompactInfo struct {
		PreTokens int `json:"preTokens"`
	} `json:"compactMetadata"`
	Origin struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Message struct {
		ID      string          `json:"id"`
		Content json.RawMessage `json:"content"`
		Model   string          `json:"model"`
	} `json:"message"`
	// Attachment is context Claude adds to a turn. A queued_command prompt
	// is a message the operator sent while the turn ran, steering it.
	Attachment struct {
		Type   string          `json:"type"`
		Prompt json.RawMessage `json:"prompt"`
		Mode   string          `json:"commandMode"`
		// SourceUUID is the uuid the message was sent with. Claude saves a
		// steered message under a uuid of its own; wi chose this one and
		// linked the send to it.
		SourceUUID string `json:"source_uuid"`
	} `json:"attachment"`
}

// steered reports a message the operator sent into a running turn.
func (r historyRecord) steered() bool {
	return r.Type == "attachment" && r.Attachment.Type == "queued_command" && (r.Attachment.Mode == "" || r.Attachment.Mode == "prompt") && len(r.Attachment.Prompt) > 0
}

type historyOffset struct {
	parent, logicalParent, typ, timestamp, reply string
	// previous is the record written just before this one on its side of
	// the transcript.
	previous string
	offset   int64
	length   int
	visible  bool
}

// next is the record before this one on its branch. A compaction boundary
// starts a new parent chain; the conversation it compacted continues before
// it. Its logical parent names that record, except that Claude sometimes
// names one written after the boundary, while compacting, which descends
// from the boundary; the record written just before the boundary is then the
// conversation's last.
func (h historyOffset) next(index map[string]historyOffset) string {
	if h.parent != "" || h.typ != "system" || h.logicalParent == "" {
		return h.parent
	}
	if logical, ok := index[h.logicalParent]; ok && logical.offset < h.offset {
		return h.logicalParent
	}
	return h.previous
}

// ReadHistoryPage indexes the native parent graph without retaining message
// bodies. Pages contain bounded content from one branch, never daemon events.
func ReadHistoryPage(ctx context.Context, path, leaf, before string) (protocol.History, error) {
	result, err := readHistoryPage(ctx, path, leaf, before, false)
	if err == nil {
		linkSubagents(path, &result)
	}
	return result, err
}

// ReadSubagentHistoryPage reads the conversation of one of the session's
// subagents, which Claude keeps beside the session's own transcript.
func ReadSubagentHistoryPage(ctx context.Context, path, agent, before string) (protocol.History, error) {
	if !subagentID.MatchString(agent) {
		return protocol.History{}, fmt.Errorf("invalid subagent identity")
	}
	transcript := filepath.Join(subagentDir(path), "agent-"+agent+".jsonl")
	if info, err := os.Lstat(transcript); err != nil || !info.Mode().IsRegular() {
		return protocol.History{}, fmt.Errorf("subagent %s has no conversation", agent)
	}
	result, err := readHistoryPage(ctx, transcript, "", before, true)
	if err == nil {
		linkSubagents(path, &result)
	}
	return result, err
}

// readHistoryPage reads the main conversation, or with sidechain a
// subagent's, whose every record is on Claude's side chain.
func readHistoryPage(ctx context.Context, path, leaf, before string, sidechain bool) (protocol.History, error) {
	result := protocol.History{Messages: []protocol.HistoryMessage{}}
	if leaf == "-" {
		return result, nil
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return result, err
	}
	reader := bufio.NewReader(io.LimitReader(file, info.Size()))
	index := map[string]historyOffset{}
	last := ""
	offset := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		line, err := historyLine(reader)
		if err == io.EOF {
			break
		}
		if err != nil {
			return result, err
		}
		var r historyRecord
		if err := json.Unmarshal(line, &r); err != nil {
			return result, fmt.Errorf("malformed Claude history record")
		}
		start := offset
		offset += int64(len(line))
		if len(r.ID) > 128 || len(r.Parent) > 128 || len(r.LogicalParent) > 128 {
			return result, fmt.Errorf("invalid Claude history identity")
		}
		if r.ID == "" || r.Sidechain != sidechain {
			continue
		}
		index[r.ID] = historyOffset{parent: r.Parent, logicalParent: r.LogicalParent, previous: last, typ: r.Type, timestamp: r.Timestamp, reply: r.Message.ID, offset: start, length: len(line), visible: (!r.Meta && (r.Type == "user" || r.Type == "assistant")) || r.steered() || r.CompactSummary || (r.Type == "system" && r.Subtype == "compact_boundary")}
		last = r.ID
	}
	if leaf == "" {
		leaf = last
	}
	result.LeafID = leaf
	seen := map[string]bool{}
	candidates := []historyOffset{}
	ids := []string{}
	found := before == ""
	for id := leaf; id != ""; {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if seen[id] {
			return result, fmt.Errorf("Claude history branch contains a cycle")
		}
		seen[id] = true
		entry, ok := index[id]
		if !ok {
			return result, fmt.Errorf("Claude history leaf or parent is not persisted")
		}
		if !found {
			if id == before {
				found = true
			}
			id = entry.next(index)
			continue
		}
		if entry.visible {
			candidates = append(candidates, entry)
			ids = append(ids, id)
		}
		id = entry.next(index)
	}
	if !found {
		return result, fmt.Errorf("history cursor is not on the selected Claude branch")
	}
	raw := []protocol.HistoryMessage{}
	// replies are the native reply IDs of raw's assistant records.
	replies := []string{}
	textRemaining := 1 << 20
	imageRemaining := 8 << 20
	for i, entry := range candidates {
		if len(raw) >= 200 || textRemaining == 0 {
			result.HasMore = true
			break
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		data := make([]byte, entry.length)
		if _, err := file.ReadAt(data, entry.offset); err != nil {
			return result, err
		}
		var record historyRecord
		if err := json.Unmarshal(data, &record); err != nil || record.ID != ids[i] {
			return result, fmt.Errorf("Claude history changed while reading")
		}
		if len(record.Timestamp) > 128 {
			return result, fmt.Errorf("invalid Claude history timestamp")
		}
		message := protocol.HistoryMessage{ID: record.ID, Role: record.Type, Timestamp: record.Timestamp, Error: record.APIError, Parts: []protocol.Part{}}
		// Claude's usage limits are its subscription's, whatever the model.
		if record.APIError && record.Error == "rate_limit" {
			message.UsageProvider = usageProvider
		}
		if record.steered() {
			message.Role = "user"
			content(record.Attachment.Prompt, &message, &result.Truncated)
			// The entry answers to the uuid it was sent with too, so the send
			// it was linked to finds it.
			if source := record.Attachment.SourceUUID; source != "" && source != record.ID && len(source) <= 128 {
				message.NativeIDs = append(message.NativeIDs, source)
			}
		} else {
			content(record.Message.Content, &message, &result.Truncated)
		}
		if message.Role == "user" {
			unpaste(&message)
		}
		if record.Origin.Kind == "task-notification" {
			taskNotification(&message)
		}
		// Claude writes a reasoning block once it ends; it began when the
		// last record before its reply, the prompt or tool result, was
		// written. Earlier blocks of the same reply do not count.
		for _, part := range message.Parts {
			if part.Thought != nil {
				before := record.Parent
				for steps := 0; steps < 64 && record.Message.ID != "" && index[before].reply == record.Message.ID; steps++ {
					before = index[before].parent
				}
				part.Thought.DurationMS = elapsedMS(index[before].timestamp, record.Timestamp)
			}
		}
		if record.CompactSummary {
			message.Role = "summary"
			message.Summary = &protocol.HistorySummary{Kind: "compaction"}
		}
		// Claude records an interrupt as a user line; it marks where the
		// operator stopped the reply, not something they wrote.
		if record.Type == "user" && interruptedContent(record.Message.Content) {
			message.Role, message.Parts, message.Aborted = "assistant", []protocol.Part{}, true
		}
		if record.Type == "system" && record.Subtype == "compact_boundary" {
			// The boundary marks the compaction; the summary is the
			// message after it, and clients show the two as one.
			message.Role = "summary"
			message.Summary = &protocol.HistorySummary{Kind: "compaction", TokensBefore: record.CompactInfo.PreTokens}
		}
		// Preserve pagination boundaries by deferring a whole message when the
		// previous messages used this page's budget. Always keep the newest entry.
		size := 0
		for _, part := range message.Parts {
			switch {
			case part.Tool != nil:
				size += len(part.Tool.Input) + len(part.Tool.Output)
			case part.Thought != nil:
				size += len(part.Thought.Text)
			default:
				size += len(part.Text)
			}
		}
		if len(raw) > 0 && size > textRemaining {
			result.HasMore = true
			break
		}
		parts := message.Parts[:0]
		for _, part := range message.Parts {
			switch {
			case part.Tool != nil:
				part.Tool.Input = historyText(part.Tool.Input, &textRemaining, &result.Truncated)
				part.Tool.Output = historyText(part.Tool.Output, &textRemaining, &result.Truncated)
				part.Tool.Images = historyImages(part.Tool.Images, &imageRemaining, &result.Truncated)
			case part.Thought != nil:
				part.Thought.Text = historyText(part.Thought.Text, &textRemaining, &result.Truncated)
			case part.Image != nil:
				if len(historyImages([]protocol.HistoryImage{*part.Image}, &imageRemaining, &result.Truncated)) == 0 {
					continue
				}
			default:
				if part.Text = historyText(part.Text, &textRemaining, &result.Truncated); part.Text == "" {
					continue
				}
			}
			parts = append(parts, part)
		}
		message.Parts = parts
		reply := ""
		if record.Type == "assistant" {
			reply = record.Message.ID
		}
		raw = append(raw, message)
		replies = append(replies, reply)
		result.BeforeID = record.ID
	}
	// A result may be on the newest page while its call is on an earlier page.
	// Recover bounded call context without adding older entries to this page.
	recoverToolContext(file, candidates[len(raw):], raw)
	type call struct {
		message int
		tool    *protocol.HistoryTool
	}
	calls := map[string]call{}
	previous := "" // the reply of the last message kept
	for i := len(raw) - 1; i >= 0; i-- {
		message := raw[i]
		result.NativeEntryIDs = append(result.NativeEntryIDs, message.ID)
		remaining := []protocol.Part{}
		for _, part := range message.Parts {
			if tool := part.Tool; tool != nil && tool.ID != "" && tool.Status != "running" {
				if at, ok := calls[tool.ID]; ok {
					at.tool.Output, at.tool.Status, at.tool.Images = tool.Output, tool.Status, tool.Images
					result.Messages[at.message].NativeIDs = append(result.Messages[at.message].NativeIDs, message.ID)
					continue
				}
			}
			remaining = append(remaining, part)
		}
		folded := len(remaining) != len(message.Parts)
		message.Parts = remaining
		if folded && len(message.Parts) == 0 {
			continue
		}
		if message.Role == "user" && message.Text() == "" && len(message.Tools()) > 0 {
			message.Role = "assistant"
		}
		at := len(result.Messages)
		// Claude writes each block of a reply as its own record; the records
		// of one reply, with only their folded results between them, are
		// one message.
		if at > 0 && replies[i] != "" && replies[i] == previous {
			at--
			target := &result.Messages[at]
			target.NativeIDs = append(append(target.NativeIDs, message.ID), message.NativeIDs...)
			target.Error = target.Error || message.Error
			if target.UsageProvider == "" {
				target.UsageProvider = message.UsageProvider
			}
			for _, part := range message.Parts {
				target.Parts = appendPart(target.Parts, part)
			}
		} else {
			result.Messages = append(result.Messages, message)
		}
		previous = replies[i]
		for _, part := range message.Parts {
			if tool := part.Tool; tool != nil && tool.ID != "" && tool.Status == "running" {
				calls[tool.ID] = call{at, tool}
			}
		}
	}
	recoverSiblingResults(file, index, seen, result.Messages, &textRemaining, &imageRemaining, &result.Truncated)
	if !result.HasMore {
		result.BeforeID = ""
	}
	return result, nil
}
func historyLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		if len(line)+len(part) > 16<<20 {
			return nil, fmt.Errorf("Claude history record exceeds 16 MiB")
		}
		line = append(line, part...)
		if err != bufio.ErrBufferFull {
			return line, err
		}
	}
}
func content(raw json.RawMessage, message *protocol.HistoryMessage, truncated *bool) {
	contentDepth(raw, message, truncated, 0)
}

// contentDepth reads a record's content blocks into the message's parts, in
// their order.
func contentDepth(raw json.RawMessage, message *protocol.HistoryMessage, truncated *bool, depth int) {
	if depth > 8 {
		*truncated = true
		return
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		message.Parts = []protocol.Part{}
		if text != "" {
			message.Parts = append(message.Parts, protocol.TextPart(text))
		}
		return
	}
	var blocks []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		Thinking string          `json:"thinking"`
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		Input    json.RawMessage `json:"input"`
		ToolID   string          `json:"tool_use_id"`
		Content  json.RawMessage `json:"content"`
		Error    bool            `json:"is_error"`
		Source   struct {
			Type string `json:"type"`
			MIME string `json:"media_type"`
			Data string `json:"data"`
		} `json:"source"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return
	}
	tools := len(message.Tools())
	for _, b := range blocks {
		switch b.Type {
		case "text":
			message.Parts = appendPart(message.Parts, protocol.TextPart(b.Text))
		case "thinking", "redacted_thinking":
			// Claude saves reasoning without its text; keep what there is.
			message.Parts = appendPart(message.Parts, protocol.ThoughtPart(protocol.Thought{Text: b.Thinking}))
		case "image":
			if b.Source.Type == "base64" {
				message.Parts = append(message.Parts, protocol.ImagePart(protocol.HistoryImage{MIMEType: b.Source.MIME, Data: b.Source.Data}))
			}
		case "tool_use":
			if tools >= 200 || len(b.ID) > 128 || len(b.Name) > 128 {
				*truncated = true
				continue
			}
			tools++
			message.Parts = append(message.Parts, protocol.ToolPart(protocol.HistoryTool{ID: b.ID, Name: b.Name, Input: string(b.Input), Status: "running"}))
		case "tool_result":
			if tools >= 200 || len(b.ToolID) > 128 {
				*truncated = true
				continue
			}
			var result protocol.HistoryMessage
			contentDepth(b.Content, &result, truncated, depth+1)
			status := "completed"
			if b.Error {
				status = "failed"
			}
			tools++
			message.Parts = append(message.Parts, protocol.ToolPart(protocol.HistoryTool{ID: b.ToolID, Output: resultText(result.Parts), Images: result.Images(), Status: status}))
		}
	}
}

// appendPart adds a part, joining text to a text part before it on a new
// line and reasoning to reasoning before it, as Claude shows adjacent
// blocks.
func appendPart(parts []protocol.Part, part protocol.Part) []protocol.Part {
	n := len(parts)
	switch {
	case part.IsText() && part.Text == "":
		return parts
	case part.IsText() && n > 0 && parts[n-1].IsText():
		parts[n-1].Text += "\n" + part.Text
		return parts
	case part.Thought != nil && n > 0 && parts[n-1].Thought != nil:
		thought := parts[n-1].Thought
		thought.Text += part.Thought.Text
		// Each block's duration runs from the reply's start to its end.
		thought.DurationMS = max(thought.DurationMS, part.Thought.DurationMS)
		return parts
	}
	return append(parts, part)
}

// resultText is a tool result's text, its text blocks on separate lines.
func resultText(parts []protocol.Part) string {
	texts := []string{}
	for _, part := range parts {
		if part.IsText() {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func historyText(text string, budget *int, truncated *bool) string {
	limit := 64 << 10
	if *budget < limit {
		limit = *budget
	}
	if len(text) > limit {
		end := limit
		for end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
		text = text[:end]
		*truncated = true
	}
	*budget -= len(text)
	return text
}
func historyImages(images []protocol.HistoryImage, budget *int, truncated *bool) []protocol.HistoryImage {
	out := []protocol.HistoryImage{}
	for _, im := range images {
		switch im.MIMEType {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
		default:
			*truncated = true
			continue
		}
		if len(im.Data) > base64.StdEncoding.EncodedLen(*budget) {
			*truncated = true
			continue
		}
		data, err := base64.StdEncoding.Strict().DecodeString(im.Data)
		if err != nil || len(data) == 0 {
			*truncated = true
			continue
		}
		*budget -= len(data)
		out = append(out, im)
	}
	return out
}

// elapsedMS is the time between two transcript timestamps, or 0 when either
// is unreadable or the gap is implausible for one reasoning step.
func elapsedMS(from, to string) int64 {
	start, err := time.Parse(time.RFC3339Nano, from)
	if err != nil {
		return 0
	}
	end, err := time.Parse(time.RFC3339Nano, to)
	if err != nil {
		return 0
	}
	elapsed := end.Sub(start)
	if elapsed <= 0 || elapsed > time.Hour {
		return 0
	}
	return elapsed.Milliseconds()
}

// Claude's TUI records text pasted into its editor, as wi delivers messages
// to it, inside a pasted_content element the harness adds for the model, and
// hands its prompt hook the same. The history shows the text as it was sent,
// and delivery compares the text sent with the text submitted.
var pastedContent = regexp.MustCompile(`(?s)<pasted_content id="([A-Za-z0-9_-]{1,32})">\n?(.*?)\n?</pasted_content(?: id="([A-Za-z0-9_-]{1,32})")?>`)

// unpasted is text with each pasted_content element replaced by what was
// pasted.
func unpasted(text string) string {
	if !strings.Contains(text, "<pasted_content id=") {
		return text
	}
	return strings.TrimSpace(pastedContent.ReplaceAllStringFunc(text, func(element string) string {
		match := pastedContent.FindStringSubmatch(element)
		if match[3] != "" && match[3] != match[1] {
			return element
		}
		return match[2]
	}))
}

func unpaste(message *protocol.HistoryMessage) {
	for i, part := range message.Parts {
		if part.Text != "" {
			message.Parts[i].Text = unpasted(part.Text)
		}
	}
}
