package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

var subagentID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// subagentDir is where Claude keeps a session's subagent transcripts and
// their metadata.
func subagentDir(path string) string {
	return filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
}

// linkSubagents gives each call that started a subagent its identity and
// state, from the metadata Claude writes beside the subagent's transcript.
func linkSubagents(path string, history *protocol.History) {
	var index map[string]protocol.HistorySubagent
	for i := range history.Messages {
		for j := range history.Messages[i].Tools {
			tool := &history.Messages[i].Tools[j]
			if tool.ID == "" || (tool.Name != "Agent" && tool.Name != "Task") {
				continue
			}
			if index == nil {
				index = subagentIndex(path)
			}
			if subagent, ok := index[tool.ID]; ok {
				tool.Subagent = &subagent
			}
		}
	}
}

// subagentIndex maps the calls that started subagents to them. Unreadable
// metadata leaves a call unlinked rather than failing its history.
func subagentIndex(path string) map[string]protocol.HistorySubagent {
	index := map[string]protocol.HistorySubagent{}
	dir := subagentDir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return index
	}
	for i, entry := range entries {
		if i >= 1024 {
			break
		}
		id, ok := strings.CutSuffix(entry.Name(), ".meta.json")
		id, prefixed := strings.CutPrefix(id, "agent-")
		if !ok || !prefixed || !subagentID.MatchString(id) || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.Size() > 64<<10 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var meta struct {
			Type        string `json:"agentType"`
			Description string `json:"description"`
			ToolID      string `json:"toolUseId"`
			Finished    bool   `json:"finished"`
		}
		if json.Unmarshal(data, &meta) != nil || meta.ToolID == "" || len(meta.ToolID) > 128 {
			continue
		}
		status := "running"
		if meta.Finished {
			status = "completed"
		}
		index[meta.ToolID] = protocol.HistorySubagent{ID: id, ToolID: meta.ToolID, Type: bounded(meta.Type, 128), Description: bounded(meta.Description, 512), Status: status}
	}
	return index
}

// taskNotification turns the message Claude's harness sends the agent when a
// background subagent stops into a report of that subagent's result.
func taskNotification(message *protocol.HistoryMessage) {
	text := message.Text
	id := tag(text, "task-id", false)
	if !strings.HasPrefix(strings.TrimSpace(text), "<task-notification>") || !subagentID.MatchString(id) {
		return
	}
	status := tag(text, "status", false)
	if status != "completed" && status != "failed" && status != "killed" {
		status = "completed"
	}
	toolID := tag(text, "tool-use-id", false)
	if len(toolID) > 128 {
		toolID = ""
	}
	message.Role = "subagent"
	message.Subagent = &protocol.HistorySubagent{ID: id, ToolID: toolID, Description: bounded(tag(text, "summary", false), 512), Status: status}
	message.Text = harnessNote.ReplaceAllString(tag(text, "result", true), "")
}

// A note the harness puts before a result it neutralized for the agent.
var harnessNote = regexp.MustCompile(`^\s*\[harness:[^\]]*\]\s*`)

// tag returns the trimmed content of the first <name> element; last extends
// it to the last closing tag, for content that may mention the tag itself.
func tag(text, name string, last bool) string {
	start := strings.Index(text, "<"+name+">")
	if start < 0 {
		return ""
	}
	start += len(name) + 2
	end := strings.Index(text[start:], "</"+name+">")
	if last {
		end = strings.LastIndex(text[start:], "</"+name+">")
	}
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(text[start : start+end])
}

func bounded(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:limit], "")
}
