package claude

import (
	"encoding/json"
	"os"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

func recoverToolContext(file *os.File, older []historyOffset, messages []protocol.HistoryMessage) {
	type at struct{ message, tool int }
	wanted := map[string][]at{}
	calls := map[string]bool{}
	budget := 1 << 20
	for _, m := range messages {
		budget -= len(m.Text)
		for _, t := range m.Tools {
			budget -= len(t.Input) + len(t.Output)
			if t.Status == "running" {
				calls[t.ID] = true
			}
		}
	}
	for i, m := range messages {
		for j, t := range m.Tools {
			if t.Status != "running" && t.ID != "" && !calls[t.ID] {
				wanted[t.ID] = append(wanted[t.ID], at{i, j})
			}
		}
	}
	readBudget := 32 << 20
	for i, entry := range older {
		if len(wanted) == 0 || i >= 1024 || entry.length > readBudget {
			break
		}
		readBudget -= entry.length
		data := make([]byte, entry.length)
		if _, err := file.ReadAt(data, entry.offset); err != nil {
			break
		}
		var record historyRecord
		if json.Unmarshal(data, &record) != nil {
			break
		}
		var message protocol.HistoryMessage
		truncated := false
		content(record.Message.Content, &message, &truncated)
		for _, tool := range message.Tools {
			if tool.Status != "running" {
				continue
			}
			locations := wanted[tool.ID]
			for _, where := range locations {
				result := &messages[where.message].Tools[where.tool]
				result.Name = tool.Name
				result.Input = historyText(tool.Input, &budget, &truncated)
			}
			delete(wanted, tool.ID)
		}
	}
}

// recoverSiblingResults completes calls whose result is off the branch.
// Claude records parallel calls one after another, and the result of an
// earlier one hangs off its call rather than off the last call, so it is a
// sibling of the branch. A result belongs to its call wherever it sits.
func recoverSiblingResults(file *os.File, index map[string]historyOffset, branch map[string]bool, messages []protocol.HistoryMessage, textBudget, imageBudget *int, truncated *bool) {
	waiting := map[string]*protocol.HistoryTool{}
	for i := range messages {
		for j := range messages[i].Tools {
			if tool := &messages[i].Tools[j]; tool.ID != "" && tool.Status == "running" {
				waiting[tool.ID] = tool
			}
		}
	}
	if len(waiting) == 0 {
		return
	}
	reads, readBudget := 0, 32<<20
	for id, entry := range index {
		if len(waiting) == 0 || reads >= 1024 {
			break
		}
		if branch[id] || !branch[entry.parent] || entry.typ != "user" || entry.length > readBudget {
			continue
		}
		reads++
		readBudget -= entry.length
		data := make([]byte, entry.length)
		if _, err := file.ReadAt(data, entry.offset); err != nil {
			return
		}
		var record historyRecord
		if json.Unmarshal(data, &record) != nil {
			continue
		}
		var message protocol.HistoryMessage
		content(record.Message.Content, &message, truncated)
		for _, result := range message.Tools {
			tool, ok := waiting[result.ID]
			if !ok || result.Status == "running" {
				continue
			}
			tool.Output = historyText(result.Output, textBudget, truncated)
			tool.Images = historyImages(result.Images, imageBudget, truncated)
			tool.Status = result.Status
			delete(waiting, result.ID)
		}
	}
}
