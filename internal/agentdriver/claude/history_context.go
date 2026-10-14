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
