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

// ReadHistory selects one branch of the native entry tree. It does not change
// the file or open an AgentSession. The live TUI can supply its in-memory leaf.
func ReadHistory(ctx context.Context, path, leaf string) (driverprotocol.History, error) {
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
	if info.Size() > 64<<20 {
		return result, fmt.Errorf("history exceeds 64 MiB read limit")
	}
	type entry struct {
		Type      string         `json:"type"`
		ID        string         `json:"id"`
		Parent    string         `json:"parentId"`
		Timestamp string         `json:"timestamp"`
		Message   map[string]any `json:"message"`
		Summary   string         `json:"summary"`
	}
	entries := map[string]entry{}
	last := ""
	reader := bufio.NewReader(io.LimitReader(f, info.Size()))
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		line, readErr := reader.ReadBytes('\n')
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return result, readErr
		}
		var e entry
		if json.Unmarshal(line, &e) != nil {
			return result, fmt.Errorf("malformed native conversation record")
		}
		if e.Type == "session" {
			continue
		}
		if e.ID == "" {
			return result, fmt.Errorf("legacy history without tree IDs is not supported")
		}
		entries[e.ID] = e
		last = e.ID
	}
	if leaf == "" {
		leaf = last
	}
	result.LeafID = leaf
	branch := []entry{}
	seen := map[string]bool{}
	for id := leaf; id != ""; {
		if seen[id] {
			return result, fmt.Errorf("conversation branch contains cycle")
		}
		seen[id] = true
		e, ok := entries[id]
		if !ok {
			return result, fmt.Errorf("conversation leaf/parent is not persisted yet")
		}
		branch = append(branch, e)
		id = e.Parent
	}
	for i, j := 0, len(branch)-1; i < j; i, j = i+1, j-1 {
		branch[i], branch[j] = branch[j], branch[i]
	}
	type toolLocation struct{ message, tool int }
	toolLocations := map[string]toolLocation{}
	total := 0
	imageTotal := 0
	for _, e := range branch {
		role, _ := e.Message["role"].(string)
		if e.Type == "message" && role == "toolResult" {
			id, _ := e.Message["toolCallId"].(string)
			name, _ := e.Message["toolName"].(string)
			failed, _ := e.Message["isError"].(bool)
			status := "completed"
			if failed {
				status = "failed"
			}
			output, _, images := historyContent(e.Message["content"])
			output = boundedHistoryText(output, &result.Truncated)
			images = boundedHistoryImages(images, &imageTotal, &result.Truncated)
			if total+len(output) > 1<<20 || len(result.Messages) >= 200 {
				result.Truncated = true
				break
			}
			if location, ok := toolLocations[id]; ok {
				tool := &result.Messages[location.message].Tools[location.tool]
				tool.Output = output
				tool.Images = images
				tool.Status = status
				total += len(output)
				continue
			}
			result.Messages = append(result.Messages, driverprotocol.HistoryMessage{ID: e.ID, Role: "assistant", Timestamp: e.Timestamp, Tools: []driverprotocol.HistoryTool{{ID: id, Name: name, Output: output, Status: status, Images: images}}})
			total += len(output)
			continue
		}
		text := ""
		tools := []driverprotocol.HistoryTool{}
		images := []driverprotocol.HistoryImage{}
		switch e.Type {
		case "message":
			if role == "system" {
				continue
			}
			text, tools, images = historyContent(e.Message["content"])
		case "compaction", "branch_summary":
			role = "summary"
			text = e.Summary
		default:
			continue
		}
		text = boundedHistoryText(text, &result.Truncated)
		images = boundedHistoryImages(images, &imageTotal, &result.Truncated)
		for i := range tools {
			tools[i].Input = boundedHistoryText(tools[i].Input, &result.Truncated)
			total += len(tools[i].Input)
		}
		if len(result.Messages) >= 200 || total+len(text) > 1<<20 {
			result.Truncated = true
			break
		}
		total += len(text)
		messageIndex := len(result.Messages)
		result.Messages = append(result.Messages, driverprotocol.HistoryMessage{ID: e.ID, Role: role, Text: text, Timestamp: e.Timestamp, Tools: tools, Images: images})
		for toolIndex, tool := range tools {
			if tool.ID != "" {
				toolLocations[tool.ID] = toolLocation{message: messageIndex, tool: toolIndex}
			}
		}
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
