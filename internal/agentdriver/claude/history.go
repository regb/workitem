package claude

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

type historyRecord struct {
	Type           string          `json:"type"`
	ID             string          `json:"uuid"`
	Parent         string          `json:"parentUuid"`
	PromptID       string          `json:"promptId"`
	Timestamp      string          `json:"timestamp"`
	Sidechain      bool            `json:"isSidechain"`
	Meta           bool            `json:"isMeta"`
	CompactSummary bool            `json:"isCompactSummary"`
	Subtype        string          `json:"subtype"`
	Summary        json.RawMessage `json:"content"`
	Message        struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}
type historyOffset struct {
	parent, typ string
	offset      int64
	length      int
	visible     bool
}

// ReadHistoryPage indexes the native parent graph without retaining message
// bodies. Pages contain bounded content from one branch, never daemon events.
func ReadHistoryPage(ctx context.Context, path, leaf, before string) (protocol.History, error) {
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
		if len(r.ID) > 128 || len(r.Parent) > 128 {
			return result, fmt.Errorf("invalid Claude history identity")
		}
		if r.ID == "" || r.Sidechain {
			continue
		}
		index[r.ID] = historyOffset{parent: r.Parent, typ: r.Type, offset: start, length: len(line), visible: (!r.Meta && (r.Type == "user" || r.Type == "assistant")) || r.CompactSummary || (r.Type == "system" && r.Subtype == "compact_boundary")}
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
			id = entry.parent
			continue
		}
		if entry.visible {
			candidates = append(candidates, entry)
			ids = append(ids, id)
		}
		id = entry.parent
	}
	if !found {
		return result, fmt.Errorf("history cursor is not on the selected Claude branch")
	}
	raw := []protocol.HistoryMessage{}
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
		message := protocol.HistoryMessage{ID: record.ID, Role: record.Type, Timestamp: record.Timestamp}
		content(record.Message.Content, &message, &result.Truncated)
		if record.CompactSummary {
			message.Role = "summary"
		}
		if record.Type == "system" && record.Subtype == "compact_boundary" {
			message.Role = "summary"
			_ = json.Unmarshal(record.Summary, &message.Text)
			if message.Text == "" {
				message.Text = "Conversation compacted"
			}
		}
		// Preserve pagination boundaries by deferring a whole message when the
		// previous messages used this page's budget. Always keep the newest entry.
		size := len(message.Text)
		for _, tool := range message.Tools {
			size += len(tool.Input) + len(tool.Output)
		}
		if len(raw) > 0 && size > textRemaining {
			result.HasMore = true
			break
		}
		message.Text = historyText(message.Text, &textRemaining, &result.Truncated)
		for j := range message.Tools {
			t := &message.Tools[j]
			t.Input = historyText(t.Input, &textRemaining, &result.Truncated)
			t.Output = historyText(t.Output, &textRemaining, &result.Truncated)
			t.Images = historyImages(t.Images, &imageRemaining, &result.Truncated)
		}
		message.Images = historyImages(message.Images, &imageRemaining, &result.Truncated)
		raw = append(raw, message)
		result.BeforeID = record.ID
	}
	// A result may be on the newest page while its call is on an earlier page.
	// Recover bounded call context without adding older entries to this page.
	recoverToolContext(file, candidates[len(raw):], raw)
	type location struct{ message, tool int }
	calls := map[string]location{}
	for i := len(raw) - 1; i >= 0; i-- {
		message := raw[i]
		result.NativeEntryIDs = append(result.NativeEntryIDs, message.ID)
		remaining := []protocol.HistoryTool{}
		for _, tool := range message.Tools {
			if at, ok := calls[tool.ID]; ok && tool.ID != "" && tool.Status != "running" {
				target := &result.Messages[at.message]
				t := &target.Tools[at.tool]
				t.Output, t.Status, t.Images = tool.Output, tool.Status, tool.Images
				target.NativeIDs = append(target.NativeIDs, message.ID)
			} else {
				remaining = append(remaining, tool)
			}
		}
		folded := len(remaining) != len(message.Tools)
		message.Tools = remaining
		if folded && message.Text == "" && len(message.Images) == 0 && len(message.Tools) == 0 {
			continue
		}
		if message.Role == "user" && message.Text == "" && len(message.Tools) > 0 {
			message.Role = "assistant"
		}
		at := len(result.Messages)
		result.Messages = append(result.Messages, message)
		for j, tool := range message.Tools {
			if tool.ID != "" && tool.Status == "running" {
				calls[tool.ID] = location{at, j}
			}
		}
	}
	if result.HasMore {
		result.Truncated = true
	} else {
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
func contentDepth(raw json.RawMessage, message *protocol.HistoryMessage, truncated *bool, depth int) {
	if depth > 8 {
		*truncated = true
		return
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		message.Text = text
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
	texts := []string{}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			texts = append(texts, b.Text)
		case "thinking":
			texts = append(texts, b.Thinking)
		case "image":
			if b.Source.Type == "base64" {
				message.Images = append(message.Images, protocol.HistoryImage{MIMEType: b.Source.MIME, Data: b.Source.Data})
			}
		case "tool_use":
			if len(message.Tools) >= 200 {
				*truncated = true
				continue
			}
			if len(b.ID) > 128 || len(b.Name) > 128 {
				*truncated = true
				continue
			}
			message.Tools = append(message.Tools, protocol.HistoryTool{ID: b.ID, Name: b.Name, Input: string(b.Input), Status: "running"})
		case "tool_result":
			if len(message.Tools) >= 200 {
				*truncated = true
				continue
			}
			var result protocol.HistoryMessage
			contentDepth(b.Content, &result, truncated, depth+1)
			if len(b.ToolID) > 128 {
				*truncated = true
				continue
			}
			status := "completed"
			if b.Error {
				status = "failed"
			}
			message.Tools = append(message.Tools, protocol.HistoryTool{ID: b.ToolID, Output: result.Text, Images: result.Images, Status: status})
		}
	}
	message.Text = strings.Join(texts, "\n")
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
