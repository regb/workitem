package claude

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func uuid(id string) string {
	h := sha256.Sum256([]byte("wi-claude:" + id))
	h[6] = (h[6] & 15) | 64
	h[8] = (h[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[:4], h[4:6], h[6:8], h[8:10], h[10:16])
}
func sessionID(path string) (string, error) {
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if !strings.HasSuffix(path, ".jsonl") || !uuidPattern.MatchString(id) {
		return "", fmt.Errorf("invalid Claude transcript locator")
	}
	return id, nil
}
func conversationOperation(r protocol.Request) (protocol.ConversationResponse, error) {
	return conversationOperationContext(context.Background(), r)
}
func conversationOperationContext(ctx context.Context, r protocol.Request) (protocol.ConversationResponse, error) {
	var result protocol.ConversationResponse
	if r.Conversation == nil {
		return result, fmt.Errorf("conversation request required")
	}
	c := r.Conversation
	switch r.Operation {
	case protocol.OperationConversationCapabilities:
		result.Capabilities = &protocol.ConversationCapabilities{History: true, HistoryPaging: true, Title: true}
	case protocol.OperationConversationHistory:
		history, err := ReadHistoryPage(ctx, c.Path, c.LeafID, c.BeforeID)
		if err != nil {
			return result, err
		}
		result.History = &history
	case protocol.OperationConversationTitle:
		config, err := parseConfig(r.Config)
		if err != nil {
			return result, err
		}
		title, err := GenerateTitle(ctx, config, c.Prompt)
		if err != nil {
			return result, err
		}
		result.Title = title
	case protocol.OperationConversationCreate:
		if c.ID == "" || strings.ContainsAny(c.ID, "/\\\x00\r\n") {
			return result, fmt.Errorf("invalid conversation identity")
		}
		result.Conversation = protocol.Conversation{ID: c.ID, Locator: "sessions/claude/" + uuid(c.ID) + ".jsonl", Format: "claude-jsonl"}
	case protocol.OperationConversationCWD:
		err := eachLine(c.Path, func(_ uint64, data []byte) error {
			var record struct {
				CWD string `json:"cwd"`
			}
			if json.Unmarshal(data, &record) == nil && record.CWD != "" {
				result.CWD = record.CWD
			}
			return nil
		})
		return result, err
	case protocol.OperationConversationFork:
		return result, fork(c.SourcePath, c.TargetPath, c.TargetCWD)
	case protocol.OperationConversationObserve:
		observed, warnings := observe(c.Path, c.Now)
		result.Observation = &observed
		result.Warnings = warnings
	case protocol.OperationConversationDecode:
		result.Decoded = make([]bool, len(c.Lines))
		for i, line := range c.Lines {
			number := c.StartLine + uint64(i)
			if len(c.LineNumbers) == len(c.Lines) {
				number = c.LineNumbers[i]
			}
			fact, valid := summarize(number, line)
			result.Decoded[i] = valid
			if valid && fact.Type != "" {
				result.Facts = append(result.Facts, fact)
			}
		}
	default:
		return result, fmt.Errorf("Claude does not support operation %s", r.Operation)
	}
	return result, nil
}
func eachLine(path string, visit func(uint64, []byte) error) error {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	scan := bufio.NewScanner(file)
	scan.Buffer(make([]byte, 4096), 16<<20)
	var line uint64
	for scan.Scan() {
		line++
		if err := visit(line, scan.Bytes()); err != nil {
			return err
		}
	}
	return scan.Err()
}
func fork(source, target, cwd string) error {
	if !filepath.IsAbs(cwd) || filepath.Clean(cwd) != cwd {
		return fmt.Errorf("invalid target workspace")
	}
	id, err := sessionID(target)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		if err == nil {
			return fmt.Errorf("fork destination already exists")
		}
		return err
	}
	if _, err := os.Stat(source); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".claude-fork-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	err = eachLine(source, func(_ uint64, line []byte) error {
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(line, &entry); err != nil {
			return fmt.Errorf("refusing to fork malformed Claude history: %w", err)
		}
		var typ string
		_ = json.Unmarshal(entry["type"], &typ)
		if typ == "queue-operation" {
			return nil
		}
		if _, ok := entry["sessionId"]; ok {
			entry["sessionId"], _ = json.Marshal(id)
		}
		if _, ok := entry["cwd"]; ok {
			entry["cwd"], _ = json.Marshal(cwd)
		}
		data, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		_, err = tmp.Write(append(data, '\n'))
		return err
	})
	if err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Publish without overwriting a concurrently created destination.
	return os.Link(tmp.Name(), target)
}
func summarize(line uint64, data []byte) (protocol.ConversationFact, bool) {
	var record struct {
		Type                string          `json:"type"`
		Timestamp           time.Time       `json:"timestamp"`
		Sidechain           bool            `json:"isSidechain"`
		Meta                bool            `json:"isMeta"`
		APIError            bool            `json:"isApiErrorMessage"`
		Error               string          `json:"error"`
		CompactSummary      bool            `json:"isCompactSummary"`
		Subtype             string          `json:"subtype"`
		PreventContinuation bool            `json:"preventContinuation"`
		Content             json.RawMessage `json:"content"`
		Message             struct {
			Role    string          `json:"role"`
			Stop    string          `json:"stop_reason"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(data, &record) != nil || record.Type == "" {
		return protocol.ConversationFact{}, false
	}
	if record.Sidechain || record.Meta || record.CompactSummary {
		return protocol.ConversationFact{}, true
	}
	var systemText string
	if record.Type == "system" && record.Subtype == "informational" && record.PreventContinuation && json.Unmarshal(record.Content, &systemText) == nil && strings.HasPrefix(systemText, "UserPromptSubmit operation blocked by hook:\n") {
		return protocol.ConversationFact{Line: line, Type: "message", Role: "assistant", Timestamp: record.Timestamp, Terminal: true, Failed: true, StopReason: "prompt_blocked"}, true
	}
	if record.Type != "user" && record.Type != "assistant" {
		return protocol.ConversationFact{}, true
	}
	fact := protocol.ConversationFact{Line: line, Type: "message", Role: record.Type, Timestamp: record.Timestamp, StopReason: record.Message.Stop, Failed: record.APIError}
	// Claude writes a synthetic reply when the API refuses a turn; a usage
	// limit waits for its reset rather than for a fix.
	if record.APIError && record.Error == "rate_limit" {
		fact.StopReason = "usage_limit"
	}
	var content []struct {
		Type  string `json:"type"`
		Error bool   `json:"is_error"`
	}
	if json.Unmarshal(record.Message.Content, &content) == nil {
		for _, block := range content {
			switch block.Type {
			case "text", "thinking", "tool_use", "tool_result", "image":
				fact.ContentTypes = append(fact.ContentTypes, block.Type)
			}
			fact.Failed = fact.Failed || block.Error
		}
	} else {
		var text string
		if json.Unmarshal(record.Message.Content, &text) == nil {
			fact.ContentTypes = []string{"text"}
		}
	}
	if record.Type == "user" && interruptedContent(record.Message.Content) {
		fact.Terminal = true
		fact.Failed = true
		fact.StopReason = "interrupted"
	} else {
		fact.Terminal = fact.Role == "assistant" && (record.APIError || (record.Message.Stop != "" && record.Message.Stop != "tool_use"))
	}
	return fact, true
}
func interruptedContent(raw json.RawMessage) bool {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &blocks) != nil || len(blocks) != 1 || blocks[0].Type != "text" {
			return false
		}
		text = blocks[0].Text
	}
	return text == "[Request interrupted by user]" || text == "[Request interrupted by user for tool use]"
}

func observe(path string, now time.Time) (protocol.ConversationObservation, []string) {
	result := protocol.ConversationObservation{Source: "claude-jsonl"}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		result.UnavailableReason = "Claude has not written a transcript yet"
		return result, nil
	}
	if err != nil {
		result.UnavailableReason = err.Error()
		return result, []string{err.Error()}
	}
	result.Exists = true
	result.SizeBytes = info.Size()
	result.ModTime = info.ModTime()
	// Observation reads only a bounded tail. Decode/indexing handles full history.
	file, err := os.Open(path)
	if err != nil {
		return result, []string{err.Error()}
	}
	defer file.Close()
	const tail = 4 << 20
	start := int64(0)
	if info.Size() > tail {
		start = info.Size() - tail
	}
	if _, err = file.Seek(start, io.SeekStart); err != nil {
		return result, []string{err.Error()}
	}
	reader := bufio.NewReader(file)
	if start > 0 {
		_, _ = reader.ReadBytes('\n')
	}
	scan := bufio.NewScanner(reader)
	scan.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if atEOF && bytes.IndexByte(data, '\n') < 0 {
			return 0, nil, nil
		}
		return bufio.ScanLines(data, atEOF)
	})
	scan.Buffer(make([]byte, 4096), tail)
	for scan.Scan() {
		result.EntriesScanned++
		fact, valid := summarize(uint64(result.EntriesScanned), scan.Bytes())
		if !valid {
			result.MalformedLines++
			continue
		}
		if fact.Type == "" {
			continue
		}
		copy := fact
		result.LastEvent = &copy
		result.LastTurnActivity = &copy
		isToolResult := false
		for _, typ := range fact.ContentTypes {
			if typ == "tool_result" {
				isToolResult = true
			}
		}
		if fact.Role == "user" && !fact.Terminal && !isToolResult {
			result.LastUserPrompt = &copy
		}
		if fact.Role == "assistant" {
			result.LastAssistantMessage = &copy
			if fact.Terminal {
				result.LastTerminalAssistant = &copy
			}
		}
		for _, typ := range fact.ContentTypes {
			if typ == "tool_use" || typ == "tool_result" {
				result.LastToolActivity = &copy
			}
		}
	}
	if scan.Err() != nil {
		return result, []string{scan.Err().Error()}
	}
	if now.IsZero() {
		now = time.Now()
	}
	if result.LastEvent != nil && !result.LastEvent.Timestamp.IsZero() {
		result.LastActivityAgeSeconds = int64(now.Sub(result.LastEvent.Timestamp).Seconds())
		if result.LastActivityAgeSeconds < 0 {
			result.LastActivityAgeSeconds = 0
		}
	}
	result.InferredTurnState = "unknown"
	if result.LastTurnActivity != nil {
		if result.LastTurnActivity.Terminal {
			result.InferredTurnState = "idle"
			if result.LastTurnActivity.Failed {
				result.InferredTurnState = "failed"
			}
		} else {
			result.InferredTurnState = "incomplete"
		}
	}
	return result, nil
}
