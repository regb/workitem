package primaryagent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/regb/workitem/internal/model"
)

type parsedPiSession struct {
	EntriesScanned        int
	MalformedLines        int
	LastEvent             *PiSessionEventSummary
	LastTurnActivity      *PiSessionEventSummary
	LastUserPrompt        *PiSessionEventSummary
	LastAssistantMessage  *PiSessionEventSummary
	LastTerminalAssistant *PiSessionEventSummary
	LastToolActivity      *PiSessionEventSummary
}

func parsePiSessionJSONL(r io.Reader) (parsedPiSession, []string) {
	warnings := []string{}
	var parsed parsedPiSession
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		event, err := summarizePiSessionLine(line, []byte(raw))
		if err != nil {
			parsed.MalformedLines++
			continue
		}
		parsed.EntriesScanned++
		parsed.LastEvent = &event
		if isPiTurnActivity(event) {
			parsed.LastTurnActivity = &event
		}
		switch {
		case event.Role == "user":
			parsed.LastUserPrompt = &event
		case event.Role == "assistant":
			parsed.LastAssistantMessage = &event
			if event.Terminal || event.Failed {
				parsed.LastTerminalAssistant = &event
			}
		case event.Role == "toolResult" || event.Type == "toolResult" || containsAny(event.ContentTypes, "toolCall", "toolResult", "bashExecution"):
			parsed.LastToolActivity = &event
		}
		if event.Role == "assistant" && containsAny(event.ContentTypes, "toolCall", "bashExecution") {
			parsed.LastToolActivity = &event
		}
	}
	if err := scanner.Err(); err != nil {
		warnings = append(warnings, "could not fully scan Pi session JSONL: "+err.Error())
	}
	return parsed, warnings
}

func isPiTurnActivity(event PiSessionEventSummary) bool {
	return event.Role == "user" || event.Role == "assistant" || event.Role == "toolResult" || event.Type == "toolResult" || containsAny(event.ContentTypes, "toolCall", "toolResult", "bashExecution")
}

type rawPiContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type rawPiSessionLine struct {
	Type       string `json:"type"`
	Timestamp  string `json:"timestamp"`
	StopReason string `json:"stopReason"` // top-level fallback
	Message    struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		StopReason string          `json:"stopReason"`
	} `json:"message"`
}

func (r rawPiSessionLine) contentBlocks() ([]rawPiContent, error) {
	raw := bytes.TrimSpace(r.Message.Content)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, err
		}
		return []rawPiContent{{Type: "text", Text: text}}, nil
	}
	var content []rawPiContent
	if err := json.Unmarshal(raw, &content); err != nil {
		return nil, err
	}
	return content, nil
}

func (r rawPiSessionLine) stopReason() string {
	return firstNonEmpty(r.Message.StopReason, r.StopReason)
}

func summarizePiSessionLine(line int, raw []byte) (PiSessionEventSummary, error) {
	var r rawPiSessionLine
	if err := json.Unmarshal(raw, &r); err != nil {
		return PiSessionEventSummary{}, err
	}
	content, err := r.contentBlocks()
	if err != nil {
		return PiSessionEventSummary{}, err
	}
	event := PiSessionEventSummary{Line: line, Type: r.Type, Role: r.Message.Role, StopReason: r.stopReason()}
	if r.Timestamp != "" {
		if ts, err := time.Parse(time.RFC3339Nano, r.Timestamp); err == nil {
			event.Timestamp = ts
		}
	}
	contentTypes := []string{}
	hasTool := false
	hasText := false
	for _, c := range content {
		if c.Type == "" {
			continue
		}
		contentTypes = append(contentTypes, c.Type)
		switch c.Type {
		case "toolCall", "bashExecution":
			hasTool = true
		case "text":
			hasText = hasText || strings.TrimSpace(c.Text) != ""
			if event.TextPreview == "" {
				event.TextPreview = truncatePreview(c.Text, 240)
			}
		}
	}
	event.ContentTypes = uniqueStrings(contentTypes)
	stop := strings.ToLower(strings.TrimSpace(event.StopReason))
	event.Failed = stop == "error" || stop == "errored" || stop == "failed" || stop == "aborted" || stop == "abort" || stop == "cancelled" || stop == "canceled" || stop == "length"
	if event.Role == "assistant" {
		event.Terminal = event.Failed || (!hasTool && hasText && (stop == "" || stop == "stop" || stop == "end_turn" || stop == "complete" || stop == "completed"))
	}
	return event, nil
}

func applyPaneInfo(workspace *model.TerminalRuntime, pane model.TerminalPaneInfo) {
	workspace.TmuxWindow = firstNonEmpty(pane.WindowName, workspace.TmuxWindow)
	workspace.TmuxPaneID = pane.PaneID
	workspace.TmuxPaneIndex = pane.PaneIndex
	workspace.TmuxPanePID = pane.PanePID
	workspace.TmuxPaneCommand = pane.Command
	workspace.TmuxPanePath = pane.CurrentPath
}

func applyPaneStatus(status *AgentProcessStatus, pane model.TerminalPaneInfo) {
	status.TmuxSession = firstNonEmpty(pane.SessionName, status.TmuxSession)
	status.TmuxWindow = firstNonEmpty(pane.WindowName, status.TmuxWindow)
	status.TmuxPaneID = pane.PaneID
	status.TmuxPaneIndex = pane.PaneIndex
	status.TmuxPanePID = pane.PanePID
	status.TmuxPaneCommand = pane.Command
	status.TmuxPanePath = pane.CurrentPath
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func containsAny(values []string, needles ...string) bool {
	for _, value := range values {
		for _, needle := range needles {
			if value == needle {
				return true
			}
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func truncatePreview(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
