package antigravity

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

// The journal is the driver's own record of a conversation: Antigravity
// keeps its sessions in a store wi does not read, so the driver writes what
// it sends and receives, one JSON record per line, at the conversation's
// locator. It is the conversation's history for wi, and names the native
// session a later runtime resumes.
const journalFormat = "antigravity-journal-v1"

// Record types.
const (
	recordSession   = "session"
	recordUser      = "user"
	recordAssistant = "assistant"
)

// Record is one journal line.
type Record struct {
	Type      string    `json:"type"`
	ID        string    `json:"id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	// Session records bind the conversation to a native session in a
	// working directory.
	SessionID string `json:"session_id,omitempty"`
	CWD       string `json:"cwd,omitempty"`
	// User records hold a prompt, linked to its sender by RequestID.
	RequestID string                  `json:"request_id,omitempty"`
	Text      string                  `json:"text,omitempty"`
	Images    []protocol.HistoryImage `json:"images,omitempty"`
	// Assistant records hold one reply: its text, reasoning, tool calls and
	// how it ended.
	Thought string                 `json:"thought,omitempty"`
	Tools   []protocol.HistoryTool `json:"tools,omitempty"`
	// Steps keep the order of a reply that called tools between its texts;
	// Text, Thought and Tools then hold all of it together.
	Steps      []Step `json:"steps,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
	Failed     bool   `json:"failed,omitempty"`
	// Reason classifies a failure, such as usage_limit.
	Reason string `json:"reason,omitempty"`
	// Error is the agent's failure text, shown in history.
	Error string `json:"error,omitempty"`
}

// Step is a stretch of a reply: text and reasoning, then the tools called
// after them.
type Step struct {
	Text    string                 `json:"text,omitempty"`
	Thought string                 `json:"thought,omitempty"`
	Tools   []protocol.HistoryTool `json:"tools,omitempty"`
}

// maxRecordBytes bounds one journal line, which can carry tool output and
// images.
const maxRecordBytes = 32 << 20

// journal appends records to a conversation file.
type journal struct {
	path string
	file *os.File
}

func openJournal(path string) (*journal, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("Antigravity journal path must be absolute")
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Antigravity journal must be a regular file")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &journal{path: path, file: file}, nil
}

// append writes one record and syncs it, so a crash loses at most the
// record being written; readers skip an incomplete last line.
func (j *journal) append(r Record) error {
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now().UTC()
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(data) >= maxRecordBytes {
		return fmt.Errorf("Antigravity journal record exceeds %d bytes", maxRecordBytes)
	}
	if _, err := j.file.Write(append(data, '\n')); err != nil {
		return err
	}
	return j.file.Sync()
}

func (j *journal) close() error { return j.file.Close() }

// readJournal visits each complete record with its 1-based line number. A
// final line without a newline is a write in progress and is skipped; a
// malformed complete line is reported to bad, when given, and skipped.
func readJournal(path string, visit func(line uint64, r Record) error, bad func(line uint64)) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64<<10)
	line := uint64(0)
	for {
		data, err := readLine(reader)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		line++
		var r Record
		if json.Unmarshal(data, &r) != nil || r.Type == "" {
			if bad != nil {
				bad(line)
			}
			continue
		}
		if err := visit(line, r); err != nil {
			return err
		}
	}
}

// readLine reads one newline-terminated line. A last line without its
// newline is incomplete and reads as io.EOF.
func readLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxRecordBytes {
			return nil, fmt.Errorf("Antigravity journal line exceeds %d bytes", maxRecordBytes)
		}
		if err == nil {
			return line[:len(line)-1], nil
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return nil, err
	}
}

// latestSession is the native session the journal last bound, and its
// working directory.
func latestSession(path string) (string, string, error) {
	session, cwd := "", ""
	err := readJournal(path, func(_ uint64, r Record) error {
		if r.Type == recordSession && r.SessionID != "" {
			session, cwd = r.SessionID, r.CWD
		}
		return nil
	}, nil)
	return session, cwd, err
}

// fact turns a record into the coordinator's compact conversation fact.
func fact(line uint64, r Record) (protocol.ConversationFact, bool) {
	switch r.Type {
	case recordUser:
		return protocol.ConversationFact{Line: line, Type: "message", Role: "user", Timestamp: r.Timestamp, ContentTypes: []string{"text"}}, true
	case recordAssistant:
		f := protocol.ConversationFact{Line: line, Type: "message", Role: "assistant", Timestamp: r.Timestamp, StopReason: r.StopReason, Failed: r.Failed, Terminal: true}
		if r.Reason != "" {
			f.StopReason = r.Reason
		}
		if r.Text != "" {
			f.ContentTypes = append(f.ContentTypes, "text")
		}
		if len(r.Tools) > 0 {
			f.ContentTypes = append(f.ContentTypes, "tool_use")
		}
		return f, true
	}
	return protocol.ConversationFact{}, false
}

// observe summarizes the journal for the coordinator.
func observe(path string, now time.Time) (protocol.ConversationObservation, []string) {
	out := protocol.ConversationObservation{Source: journalFormat}
	info, err := os.Stat(path)
	if err != nil {
		out.UnavailableReason = "conversation has no journal yet"
		return out, nil
	}
	out.Exists, out.SizeBytes, out.ModTime = true, info.Size(), info.ModTime()
	err = readJournal(path, func(line uint64, r Record) error {
		out.EntriesScanned++
		f, ok := fact(line, r)
		if !ok {
			return nil
		}
		copy := f
		out.LastEvent, out.LastTurnActivity = &copy, &copy
		if f.Role == "user" {
			out.LastUserPrompt = &copy
		} else {
			out.LastAssistantMessage, out.LastTerminalAssistant = &copy, &copy
		}
		return nil
	}, func(uint64) { out.MalformedLines++ })
	if err != nil {
		return out, []string{"read Antigravity journal: " + err.Error()}
	}
	switch {
	case out.LastTurnActivity == nil:
		out.InferredTurnState = "idle"
	case out.LastTurnActivity.Role == "user":
		out.InferredTurnState = "incomplete"
	case out.LastTerminalAssistant.Failed:
		out.InferredTurnState = "failed"
	default:
		out.InferredTurnState = "idle"
	}
	if out.LastTurnActivity != nil && !now.IsZero() {
		out.LastActivityAgeSeconds = int64(now.Sub(out.LastTurnActivity.Timestamp).Seconds())
	}
	return out, nil
}

// historyPage is the latest page of up to limit messages before beforeID.
func historyPage(path, beforeID string, limit int) (protocol.History, error) {
	messages := []protocol.HistoryMessage{}
	err := readJournal(path, func(_ uint64, r Record) error {
		switch r.Type {
		case recordUser:
			messages = append(messages, protocol.HistoryMessage{ID: r.ID, NativeIDs: []string{r.ID}, Role: "user", Text: r.Text, Images: r.Images, Timestamp: r.Timestamp.Format(time.RFC3339Nano)})
		case recordAssistant:
			messages = append(messages, assistantMessages(r)...)
		}
		return nil
	}, nil)
	if err != nil {
		return protocol.History{}, err
	}
	end := len(messages)
	if beforeID != "" {
		end = -1
		for i, m := range messages {
			if m.ID == beforeID {
				end = i
			}
		}
		if end < 0 {
			return protocol.History{}, fmt.Errorf("unknown history entry %q", beforeID)
		}
	}
	start := end - limit
	if start < 0 {
		start = 0
	}
	page := protocol.History{Messages: messages[start:end]}
	if page.Messages == nil {
		page.Messages = []protocol.HistoryMessage{}
	}
	if start > 0 {
		page.HasMore, page.BeforeID = true, messages[start].ID
	}
	if n := len(messages); n > 0 {
		page.LeafID = messages[n-1].ID
	}
	for _, m := range page.Messages {
		page.NativeEntryIDs = append(page.NativeEntryIDs, m.ID)
	}
	return page, nil
}

// assistantMessages are a reply's history messages: one per step, in order,
// the last one carrying the reply's ID and how it ended.
func assistantMessages(r Record) []protocol.HistoryMessage {
	steps := r.Steps
	if len(steps) == 0 {
		steps = []Step{{Text: r.Text, Thought: r.Thought, Tools: r.Tools}}
	}
	timestamp := r.Timestamp.Format(time.RFC3339Nano)
	out := make([]protocol.HistoryMessage, 0, len(steps))
	for i, step := range steps {
		m := protocol.HistoryMessage{ID: fmt.Sprintf("%s.%d", r.ID, i), NativeIDs: []string{r.ID}, Role: "assistant", Text: step.Text, Tools: step.Tools, Timestamp: timestamp}
		if step.Thought != "" {
			m.Reasoning = &protocol.HistoryReasoning{Text: step.Thought}
		}
		if i == len(steps)-1 {
			m.ID, m.Aborted = r.ID, r.StopReason == "cancelled"
			if r.Failed && r.Text == "" && r.Error != "" {
				m.Text, m.Error = r.Error, true
			}
		}
		out = append(out, m)
	}
	return out
}

// forkJournal copies a journal for a checkout moved to another directory:
// the native session is loaded again in targetCWD.
func forkJournal(source, target, targetCWD string) error {
	if !filepath.IsAbs(source) || !filepath.IsAbs(target) || !filepath.IsAbs(targetCWD) {
		return fmt.Errorf("Antigravity fork requires absolute paths")
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("Antigravity fork target already exists")
	}
	out, err := openJournal(target)
	if err != nil {
		return err
	}
	defer out.close()
	return readJournal(source, func(_ uint64, r Record) error {
		if r.Type == recordSession {
			r.CWD = targetCWD
		}
		return out.append(r)
	}, nil)
}
