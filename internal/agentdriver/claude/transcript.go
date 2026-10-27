package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

// transcriptFeed follows complete JSONL records with bounded pending input.
// It never publishes native text to the runtime event socket.
type transcriptFeed struct {
	offset                 int64
	factAfter              time.Time
	promptID, textPromptID string
	textAt                 time.Time
	info                   os.FileInfo
	pending                []byte
	leaf, text, activity   string
	textID                 string
	// model and effort are those of the latest reply made since factAfter.
	model, effort string
	tool          *protocol.ContentTool
}

type transcriptPublisher interface {
	PublishTranscript(string, string, string, *protocol.ContentTool)
}

// ignoredTranscript reads a transcript only for its facts.
type ignoredTranscript struct{}

func (ignoredTranscript) PublishTranscript(string, string, string, *protocol.ContentTool) {}

func (f *transcriptFeed) refresh(path string, server transcriptPublisher) ([]protocol.ConversationFact, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	initial := f.info == nil
	if initial || !os.SameFile(f.info, info) || info.Size() < f.offset {
		*f = transcriptFeed{info: info, activity: "Transcript available", factAfter: f.factAfter, model: f.model, effort: f.effort}
		if info.Size() > 4<<20 {
			f.offset = info.Size() - (4 << 20)
			if _, err = file.Seek(f.offset, io.SeekStart); err != nil {
				return nil, err
			}
			buf := make([]byte, 4096)
			for {
				n, e := file.Read(buf)
				at := bytes.IndexByte(buf[:n], '\n')
				if at >= 0 {
					f.offset += int64(at + 1)
					break
				}
				f.offset += int64(n)
				if e != nil {
					break
				}
			}
		}
	}
	if _, err := file.Seek(f.offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, 4<<20))
	if err != nil {
		return nil, err
	}
	f.offset += int64(len(data))
	f.info = info
	f.pending = append(f.pending, data...)
	facts := []protocol.ConversationFact{}
	for {
		at := bytes.IndexByte(f.pending, '\n')
		if at < 0 {
			break
		}
		if at > 16<<20 {
			return nil, fmt.Errorf("Claude transcript record exceeds 16 MiB")
		}
		line := f.pending[:at]
		f.pending = f.pending[at+1:]
		var record historyRecord
		if json.Unmarshal(line, &record) != nil {
			return nil, fmt.Errorf("malformed Claude transcript record")
		}
		if record.Sidechain || record.Meta {
			continue
		}
		if len(record.ID) > 128 {
			return nil, fmt.Errorf("invalid Claude transcript identity")
		}
		if record.ID != "" {
			f.leaf = record.ID
		}
		if record.Type == "user" || record.Type == "assistant" {
			var message protocol.HistoryMessage
			truncated := false
			content(record.Message.Content, &message, &truncated)
			if record.Type == "assistant" {
				f.text = message.Text
				f.textID = record.ID
				f.textPromptID = f.promptID
				f.textAt, _ = time.Parse(time.RFC3339Nano, record.Timestamp)
				f.activity = "Message saved"
				if !f.textAt.IsZero() && !f.textAt.Before(f.factAfter.Truncate(time.Millisecond)) {
					if record.Message.Model != "" {
						f.model = record.Message.Model
					}
					if record.Effort != "" {
						f.effort = record.Effort
					}
				}
			} else if len(message.Tools) == 0 && !record.CompactSummary {
				f.text = ""
				if len(record.PromptID) <= 128 {
					f.promptID = record.PromptID
				}
				f.textPromptID = ""
				f.textAt = time.Time{}
				f.tool = nil
				f.activity = "Working"
			}
			for _, tool := range message.Tools {
				if tool.Status != "running" && f.tool != nil && f.tool.ID == tool.ID {
					tool.Name = f.tool.Name
					tool.Input = f.tool.Input
				}
				f.tool = &protocol.ContentTool{ID: tool.ID, Name: tool.Name, Input: tool.Input, Output: tool.Output, Status: tool.Status}
				if tool.Status == "running" {
					f.activity = "Tool running: " + tool.Name
				} else {
					f.activity = "Tool " + tool.Status
				}
			}
			fact, valid := summarize(1, line)
			if valid && fact.Terminal && fact.Failed {
				f.activity = "Turn failed or interrupted"
				if !initial || !f.factAfter.IsZero() && !fact.Timestamp.IsZero() && !fact.Timestamp.Before(f.factAfter.Truncate(time.Millisecond)) {
					facts = append(facts, fact)
				}
			}
		}
		if record.Type == "system" {
			fact, valid := summarize(1, line)
			if valid && fact.Terminal && fact.Failed {
				f.text = ""
				f.tool = nil
				f.activity = "Prompt blocked by user hook"
				if !initial || !f.factAfter.IsZero() && !fact.Timestamp.IsZero() && !fact.Timestamp.Before(f.factAfter.Truncate(time.Millisecond)) {
					facts = append(facts, fact)
				}
			}
		}
		if publisher, ok := server.(interface {
			PublishPromptTranscript(string, string, string, *protocol.ContentTool, string, time.Time)
		}); ok {
			publisher.PublishPromptTranscript(f.leaf, f.text, f.activity, f.tool, f.textPromptID, f.textAt)
		} else {
			server.PublishTranscript(f.leaf, f.text, f.activity, f.tool)
		}
		// Link the shown text to its record; the server ignores it unless that
		// text is what readers currently see.
		if saver, ok := server.(interface{ MarkSaved(string, []string) }); ok && f.text != "" && f.textID != "" {
			saver.MarkSaved(f.text, []string{f.textID})
		}
	}
	if len(f.pending) > 16<<20 {
		return nil, fmt.Errorf("Claude transcript record exceeds 16 MiB")
	}
	// Do not keep a large backing array after processing a large record.
	f.pending = append([]byte(nil), f.pending...)
	return facts, nil
}
