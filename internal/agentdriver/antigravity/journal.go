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
	// Assistant records hold one reply: its text, reasoning and tool calls
	// as parts, in the order agy wrote them, and how it ended.
	Parts []protocol.Part `json:"parts,omitempty"`
	// Older journals hold a reply's text in Text, with its reasoning and
	// calls in Thought and Tools, or in Steps when it called tools between
	// its texts.
	Thought    string                 `json:"thought,omitempty"`
	Tools      []protocol.HistoryTool `json:"tools,omitempty"`
	Steps      []Step                 `json:"steps,omitempty"`
	StopReason string                 `json:"stop_reason,omitempty"`
	Failed     bool                   `json:"failed,omitempty"`
	// Reason classifies a failure, such as usage_limit.
	Reason string `json:"reason,omitempty"`
	// Error is the agent's failure text, shown in history.
	Error string `json:"error,omitempty"`
}

// Step is a stretch of an older journal's reply: text and reasoning, then
// the tools called after them.
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
			f.StopReason, f.UsageProvider = r.Reason, reasonUsageProvider(r.Reason)
		}
		parts := replyParts(r)
		if protocol.PartsText(parts) != "" {
			f.ContentTypes = append(f.ContentTypes, "text")
		}
		if len(protocol.PartsTools(parts)) > 0 {
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
			m := protocol.HistoryMessage{ID: r.ID, NativeIDs: []string{r.ID}, Role: "user", Parts: []protocol.Part{}, Timestamp: r.Timestamp.Format(time.RFC3339Nano)}
			if r.Text != "" {
				m.Parts = append(m.Parts, protocol.TextPart(r.Text))
			}
			for _, image := range r.Images {
				m.Parts = append(m.Parts, protocol.ImagePart(image))
			}
			messages = append(messages, m)
		case recordAssistant:
			messages = append(messages, assistantMessage(r))
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

// usageProvider is the provider whose quota a usage limit reports, as the
// usage plugin names it: agy's quota belongs to the Google account, whatever
// the model.
const usageProvider = "antigravity"

// reasonUsageProvider names the provider of a usage_limit reason.
func reasonUsageProvider(reason string) string {
	if reason == "usage_limit" {
		return usageProvider
	}
	return ""
}

// assistantMessage is a reply's history message, its parts in order.
func assistantMessage(r Record) protocol.HistoryMessage {
	m := protocol.HistoryMessage{ID: r.ID, NativeIDs: []string{r.ID}, Role: "assistant", Parts: replyParts(r), Timestamp: r.Timestamp.Format(time.RFC3339Nano), Aborted: r.StopReason == "cancelled"}
	if r.Failed && protocol.PartsText(m.Parts) == "" {
		text := r.Error
		// agy refuses a turn for an exhausted quota without saying so.
		if text == "" && r.Reason == "usage_limit" {
			text = "Antigravity usage limit reached."
		}
		if text != "" {
			m.Parts, m.Error = append(m.Parts, protocol.TextPart(text)), true
			m.UsageProvider = reasonUsageProvider(r.Reason)
		}
	}
	return m
}

// replyParts are a reply's parts. Older journals do not say where reasoning
// came within a step; it goes before the step's text, as models reason
// before they write.
func replyParts(r Record) []protocol.Part {
	if r.Parts != nil {
		return r.Parts
	}
	steps := r.Steps
	if len(steps) == 0 {
		steps = []Step{{Text: r.Text, Thought: r.Thought, Tools: r.Tools}}
	}
	parts := []protocol.Part{}
	for _, step := range steps {
		if step.Thought != "" {
			parts = append(parts, protocol.ThoughtPart(protocol.Thought{Text: step.Thought}))
		}
		if step.Text != "" {
			parts = append(parts, protocol.TextPart(step.Text))
		}
		for _, tool := range step.Tools {
			parts = append(parts, protocol.ToolPart(tool))
		}
	}
	return parts
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
