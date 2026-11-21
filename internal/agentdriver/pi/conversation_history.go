package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
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
	// TokensBefore is a compaction's context size before it ran.
	TokensBefore int `json:"tokensBefore"`
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
		message := driverprotocol.HistoryMessage{ID: entry.ID, Timestamp: entry.Timestamp, Parts: []driverprotocol.Part{}}
		if entry.Type == "message" {
			message.Role, _ = entry.Message["role"].(string)
			message.Parts = historyParts(entry.Message["content"])
			stop, _ := entry.Message["stopReason"].(string)
			if message.Role == "assistant" && abortedStop(stop) {
				message.Aborted = true
			}
			// Pi records a provider failure, such as a usage limit, as an
			// empty reply carrying the error: show it as the harness's notice.
			if failure, _ := entry.Message["errorMessage"].(string); message.Role == "assistant" && stop == "error" && strings.TrimSpace(failure) != "" && strings.TrimSpace(message.Text()) == "" {
				message.Error = true
				message.Parts = append(message.Parts, driverprotocol.TextPart(strings.TrimSpace(failure)))
				if usageLimitError(failure) {
					provider, _ := entry.Message["provider"].(string)
					message.UsageProvider = usageProvider(provider)
				}
			}
			// Pi notes a resized image in the prompt's text; the operator never
			// wrote it, so history shows the message as sent.
			if message.Role == "user" {
				parts := []driverprotocol.Part{}
				for _, part := range message.Parts {
					if part.IsText() {
						if part.Text = strings.TrimSpace(piImageNote.ReplaceAllString(part.Text, "")); part.Text == "" {
							continue
						}
					}
					parts = append(parts, part)
				}
				message.Parts = parts
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
				message.Parts = []driverprotocol.Part{driverprotocol.ToolPart(driverprotocol.HistoryTool{ID: id, Name: name, Output: message.Text(), Status: status, Images: message.Images()})}
			}
			if message.Role == "bashExecution" {
				message.Role = "user"
				message.Parts = []driverprotocol.Part{driverprotocol.ToolPart(operatorCommand(entry.Message))}
			}
		} else {
			message.Role = "summary"
			if text := summaryMarkdown(entry.Summary); text != "" {
				message.Parts = append(message.Parts, driverprotocol.TextPart(text))
			}
			message.Summary = &driverprotocol.HistorySummary{Kind: "compaction", TokensBefore: entry.TokensBefore}
			if entry.Type == "branch_summary" {
				message.Summary = &driverprotocol.HistorySummary{Kind: "branch"}
			}
		}
		size, toolCount := 0, 0
		parts := message.Parts[:0]
		for _, part := range message.Parts {
			switch {
			case part.Tool != nil:
				// Keep the first calls of a reply that made very many.
				if toolCount++; toolCount > 200 {
					result.Truncated = true
					continue
				}
				part.Tool.Input = boundedHistoryText(part.Tool.Input, &result.Truncated)
				part.Tool.Output = boundedHistoryText(part.Tool.Output, &result.Truncated)
			case part.Thought != nil:
				part.Thought.Text = boundedHistoryText(part.Thought.Text, &result.Truncated)
				size += len(part.Thought.Text)
			case part.IsText():
				part.Text = boundedHistoryText(part.Text, &result.Truncated)
				size += len(part.Text)
			}
			parts = append(parts, part)
		}
		message.Parts = parts
		textSize := size
		for _, tool := range message.Tools() {
			size += len(tool.Input) + len(tool.Output)
		}
		// Keep the newest entry even if it has many tools, with explicit truncation.
		if size > 1<<20 && len(rawMessages) == 0 {
			remaining := (1 << 20) - textSize
			for _, part := range message.Parts {
				if part.Tool == nil {
					continue
				}
				for _, field := range []*string{&part.Tool.Input, &part.Tool.Output} {
					if len(*field) > remaining {
						end := max(remaining, 0)
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
		parts = message.Parts[:0]
		for _, part := range message.Parts {
			switch {
			case part.Image != nil:
				if len(boundedHistoryImages([]driverprotocol.HistoryImage{*part.Image}, &imageTotal, &result.Truncated)) == 0 {
					continue
				}
			case part.Tool != nil:
				part.Tool.Images = boundedHistoryImages(part.Tool.Images, &imageTotal, &result.Truncated)
			}
			parts = append(parts, part)
		}
		message.Parts = parts
		textTotal += size
		rawMessages = append(rawMessages, message)
		result.BeforeID = e.ID
		if index == len(candidates)-1 {
			result.HasMore = false
		}
	}
	// Normalize the selected page chronologically and fold native tool-result
	// records into calls. Retain aliases so clients can reconcile native anchors.
	type toolLocation struct{ message, part int }
	locations := map[string]toolLocation{}
	for i := len(rawMessages) - 1; i >= 0; i-- {
		message := rawMessages[i]
		result.NativeEntryIDs = append(result.NativeEntryIDs, message.ID)
		if len(message.Parts) == 1 && message.Parts[0].Tool != nil && message.Parts[0].Tool.Status != "running" {
			tool := message.Parts[0].Tool
			if location, ok := locations[tool.ID]; ok && tool.ID != "" {
				target := &result.Messages[location.message]
				call := target.Parts[location.part].Tool
				call.Output, call.Images, call.Status = tool.Output, tool.Images, tool.Status
				target.NativeIDs = append(target.NativeIDs, message.ID)
				continue
			}
		}
		index := len(result.Messages)
		result.Messages = append(result.Messages, message)
		for j, part := range message.Parts {
			if part.Tool != nil && part.Tool.ID != "" {
				locations[part.Tool.ID] = toolLocation{index, j}
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

// piImageNote is the note Pi adds to a prompt whose image it resized.
var piImageNote = regexp.MustCompile(`\n*\[Image: original \d+x\d+[^\]\n]*\]`)

// historyParts reads a message's content blocks as parts, in their order.
// Adjacent text blocks join with a line break and adjacent reasoning blocks
// with a blank line, as one stretch each.
func historyParts(content any) []driverprotocol.Part {
	parts := []driverprotocol.Part{}
	if s, ok := content.(string); ok {
		return appendBlockText(parts, s)
	}
	blocks, _ := content.([]any)
	for _, raw := range blocks {
		b, _ := raw.(map[string]any)
		typ, _ := b["type"].(string)
		switch typ {
		case "text":
			s, _ := b["text"].(string)
			parts = appendBlockText(parts, s)
		case "toolCall":
			id, _ := b["id"].(string)
			name, _ := b["name"].(string)
			input := []byte(nil)
			if arguments, ok := b["arguments"]; ok {
				input, _ = json.MarshalIndent(arguments, "", "  ")
			}
			parts = append(parts, driverprotocol.ToolPart(driverprotocol.HistoryTool{ID: id, Name: name, Input: string(input), Status: "running"}))
		case "image":
			data, _ := b["data"].(string)
			mimeType, _ := b["mimeType"].(string)
			if data != "" && strings.HasPrefix(mimeType, "image/") {
				parts = append(parts, driverprotocol.ImagePart(driverprotocol.HistoryImage{MIMEType: mimeType, Data: data, Alt: "Attached image"}))
			}
		case "thinking":
			// Reasoning a model keeps private still shows that it reasoned.
			s, _ := b["thinking"].(string)
			if n := len(parts); n > 0 && parts[n-1].Thought != nil {
				if s != "" {
					thought := *parts[n-1].Thought
					thought.Text = strings.TrimPrefix(thought.Text+"\n\n"+s, "\n\n")
					parts[n-1] = driverprotocol.ThoughtPart(thought)
				}
				continue
			}
			parts = append(parts, driverprotocol.ThoughtPart(driverprotocol.Thought{Text: s}))
		default:
			if typ != "" {
				parts = appendBlockText(parts, "["+typ+" content omitted]")
			}
		}
	}
	return parts
}

// appendBlockText adds a text block, on its own line after a text block
// before it.
func appendBlockText(parts []driverprotocol.Part, text string) []driverprotocol.Part {
	if text == "" {
		return parts
	}
	if n := len(parts); n > 0 && parts[n-1].IsText() {
		parts[n-1].Text += "\n" + text
		return parts
	}
	return append(parts, driverprotocol.TextPart(text))
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

// summaryFiles is a file list Pi appends to a summary, in tags that
// Markdown would drop as HTML.
var summaryFiles = regexp.MustCompile(`(?s)<(read|modified)-files>\n?(.*?)\n?</(?:read|modified)-files>`)

// summaryMarkdown turns a summary's file lists into Markdown lists.
func summaryMarkdown(summary string) string {
	return summaryFiles.ReplaceAllStringFunc(summary, func(block string) string {
		match := summaryFiles.FindStringSubmatch(block)
		heading := "Read files"
		if match[1] == "modified" {
			heading = "Modified files"
		}
		var list strings.Builder
		for _, file := range strings.Split(match[2], "\n") {
			if file = strings.TrimSpace(file); file != "" {
				fmt.Fprintf(&list, "- `%s`\n", strings.ReplaceAll(file, "`", "'"))
			}
		}
		return "**" + heading + "**\n\n" + list.String()
	})
}

// operatorCommand is a shell command the operator ran from Pi's prompt
// (`!ls`), shown as the bash call it is.
func operatorCommand(native map[string]any) driverprotocol.HistoryTool {
	command, _ := native["command"].(string)
	output, _ := native["output"].(string)
	exit, _ := native["exitCode"].(float64)
	cancelled, _ := native["cancelled"].(bool)
	input, _ := json.Marshal(map[string]string{"command": command})
	status := "completed"
	if exit != 0 || cancelled {
		status = "failed"
	}
	return driverprotocol.HistoryTool{Name: "bash", Input: string(input), Output: output, Status: status}
}
