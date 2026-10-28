package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
)

type historyEntry struct {
	Type      string         `json:"type"`
	ID        string         `json:"id"`
	Parent    string         `json:"parentId"`
	Timestamp string         `json:"timestamp"`
	Message   map[string]any `json:"message"`
	Summary   string         `json:"summary"`
}
type historyIndexEntry struct {
	ID, Parent, Type, Role string
	Offset                 int64
	Length                 int
}

// ReadHistory returns the latest page, never the beginning of a long conversation.
func ReadHistory(ctx context.Context, path, leaf string) (driverprotocol.History, error) {
	return ReadHistoryPage(ctx, path, leaf, "")
}

// ReadHistoryPage indexes native IDs and offsets rather than retaining transcript
// bodies. There is no conversation-size limit. Each response is bounded; before
// is an exclusive native cursor on the selected branch, not a filesystem path.
func ReadHistoryPage(ctx context.Context, path, leaf, before string) (driverprotocol.History, error) {
	result := driverprotocol.History{Messages: []driverprotocol.HistoryMessage{}}
	if leaf == "-" {
		return result, nil
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return result, err
	}
	entries := map[string]historyIndexEntry{}
	last := ""
	var offset int64
	reader := bufio.NewReader(io.LimitReader(f, info.Size()))
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		line, readErr := reader.ReadBytes('\n')
		if readErr == io.EOF {
			break
		} // Ignore a concurrently appended incomplete record.
		if readErr != nil {
			return result, readErr
		}
		var metadata struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Parent  string `json:"parentId"`
			Message struct {
				Role string `json:"role"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &metadata) != nil {
			return result, fmt.Errorf("malformed native conversation record")
		}
		start := offset
		offset += int64(len(line))
		if metadata.Type == "session" {
			continue
		}
		if metadata.ID == "" {
			return result, fmt.Errorf("legacy history without tree IDs is not supported")
		}
		entries[metadata.ID] = historyIndexEntry{metadata.ID, metadata.Parent, metadata.Type, metadata.Message.Role, start, len(line)}
		last = metadata.ID
	}
	if leaf == "" {
		leaf = last
	}
	result.LeafID = leaf
	seen := map[string]bool{}
	candidates := []historyIndexEntry{}
	foundBefore := before == ""
	for id := leaf; id != ""; {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if seen[id] {
			return result, fmt.Errorf("conversation branch contains cycle")
		}
		seen[id] = true
		e, ok := entries[id]
		if !ok {
			return result, fmt.Errorf("conversation leaf/parent is not persisted yet")
		}
		if !foundBefore {
			if id == before {
				foundBefore = true
			}
			id = e.Parent
			continue
		}
		if (e.Type == "message" && e.Role != "system") || e.Type == "compaction" || e.Type == "branch_summary" {
			candidates = append(candidates, e)
		}
		id = e.Parent
	}
	if !foundBefore {
		return result, fmt.Errorf("history cursor is not on the selected branch")
	}
	rawMessages := []driverprotocol.HistoryMessage{}
	textTotal, imageTotal := 0, 0
	for index, e := range candidates {
		if len(rawMessages) >= 200 {
			result.HasMore = true
			break
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		line := make([]byte, e.Length)
		if _, err := f.ReadAt(line, e.Offset); err != nil {
			return result, err
		}
		var entry historyEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return result, fmt.Errorf("native history changed while reading: %w", err)
		}
		message := driverprotocol.HistoryMessage{ID: entry.ID, Timestamp: entry.Timestamp}
		if entry.Type == "message" {
			message.Role, _ = entry.Message["role"].(string)
			message.Text, message.Tools, message.Images = historyContent(entry.Message["content"])
			if stop, _ := entry.Message["stopReason"].(string); message.Role == "assistant" && abortedStop(stop) {
				message.Aborted = true
			}
			if message.Role == "toolResult" {
				id, _ := entry.Message["toolCallId"].(string)
				name, _ := entry.Message["toolName"].(string)
				failed, _ := entry.Message["isError"].(bool)
				status := "completed"
				if failed {
					status = "failed"
				}
				message.Role = "assistant"
				message.Tools = []driverprotocol.HistoryTool{{ID: id, Name: name, Output: message.Text, Status: status, Images: message.Images}}
				message.Text = ""
				message.Images = nil
			}
		} else {
			message.Role = "summary"
			message.Text = entry.Summary
		}
		message.Text = boundedHistoryText(message.Text, &result.Truncated)
		size := len(message.Text)
		if len(message.Tools) > 200 {
			message.Tools = message.Tools[:200]
			result.Truncated = true
		}
		for i := range message.Tools {
			tool := &message.Tools[i]
			tool.Input = boundedHistoryText(tool.Input, &result.Truncated)
			tool.Output = boundedHistoryText(tool.Output, &result.Truncated)
			size += len(tool.Input) + len(tool.Output)
		}
		// Keep the newest entry even if it has many tools, with explicit truncation.
		if size > 1<<20 && len(rawMessages) == 0 {
			remaining := (1 << 20) - len(message.Text)
			for i := range message.Tools {
				tool := &message.Tools[i]
				for _, field := range []*string{&tool.Input, &tool.Output} {
					if len(*field) > remaining {
						end := remaining
						for end > 0 && !utf8.RuneStart((*field)[end]) {
							end--
						}
						*field = (*field)[:end]
						result.Truncated = true
					}
					remaining -= len(*field)
				}
			}
			size = (1 << 20) - remaining
		}
		if textTotal+size > 1<<20 {
			result.HasMore = true
			break
		}
		message.Images = boundedHistoryImages(message.Images, &imageTotal, &result.Truncated)
		for i := range message.Tools {
			message.Tools[i].Images = boundedHistoryImages(message.Tools[i].Images, &imageTotal, &result.Truncated)
		}
		textTotal += size
		rawMessages = append(rawMessages, message)
		result.BeforeID = e.ID
		if index == len(candidates)-1 {
			result.HasMore = false
		}
	}
	// Normalize the selected page chronologically and fold native tool-result
	// records into calls. Retain aliases so clients can reconcile native anchors.
	type toolLocation struct{ message, tool int }
	locations := map[string]toolLocation{}
	for i := len(rawMessages) - 1; i >= 0; i-- {
		message := rawMessages[i]
		result.NativeEntryIDs = append(result.NativeEntryIDs, message.ID)
		if message.Text == "" && len(message.Tools) == 1 && message.Tools[0].Status != "running" {
			tool := message.Tools[0]
			if location, ok := locations[tool.ID]; ok && tool.ID != "" {
				target := &result.Messages[location.message]
				target.Tools[location.tool].Output = tool.Output
				target.Tools[location.tool].Images = tool.Images
				target.Tools[location.tool].Status = tool.Status
				target.NativeIDs = append(target.NativeIDs, message.ID)
				continue
			}
		}
		index := len(result.Messages)
		result.Messages = append(result.Messages, message)
		for j, tool := range message.Tools {
			if tool.ID != "" {
				locations[tool.ID] = toolLocation{index, j}
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

func historyContent(content any) (string, []driverprotocol.HistoryTool, []driverprotocol.HistoryImage) {
	if s, ok := content.(string); ok {
		return s, nil, nil
	}
	blocks, _ := content.([]any)
	parts := []string{}
	tools := []driverprotocol.HistoryTool{}
	images := []driverprotocol.HistoryImage{}
	for _, raw := range blocks {
		b, _ := raw.(map[string]any)
		typ, _ := b["type"].(string)
		switch typ {
		case "text":
			s, _ := b["text"].(string)
			parts = append(parts, s)
		case "toolCall":
			id, _ := b["id"].(string)
			name, _ := b["name"].(string)
			input := []byte(nil)
			if arguments, ok := b["arguments"]; ok {
				input, _ = json.MarshalIndent(arguments, "", "  ")
			}
			tools = append(tools, driverprotocol.HistoryTool{ID: id, Name: name, Input: string(input), Status: "running"})
		case "image":
			data, _ := b["data"].(string)
			mimeType, _ := b["mimeType"].(string)
			if data != "" && strings.HasPrefix(mimeType, "image/") {
				images = append(images, driverprotocol.HistoryImage{MIMEType: mimeType, Data: data, Alt: "Attached image"})
			}
		case "thinking":
			// Deliberation is private and intentionally absent from history.
		default:
			if typ != "" {
				parts = append(parts, "["+typ+" content omitted]")
			}
		}
	}
	return strings.Join(parts, "\n"), tools, images
}

func boundedHistoryImages(images []driverprotocol.HistoryImage, total *int, truncated *bool) []driverprotocol.HistoryImage {
	kept := make([]driverprotocol.HistoryImage, 0, len(images))
	for _, image := range images {
		if len(image.Data) > 12<<20 || *total+len(image.Data) > 12<<20 {
			*truncated = true
			continue
		}
		*total += len(image.Data)
		kept = append(kept, image)
	}
	return kept
}

func boundedHistoryText(text string, truncated *bool) string {
	if len(text) <= 16<<10 {
		return text
	}
	end := 16 << 10
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	*truncated = true
	return text[:end] + "\n[content truncated]"
}
