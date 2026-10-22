package driverprotocol

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// ContentVersion is independent of the process bootstrap and runtime protocols.
const ContentVersion = 1

const MaxContentTextBytes = 256 << 10

// Content is an ephemeral, read-only snapshot, never a retained runtime event.
// Text contains the currently streaming assistant message, or the latest
// persisted assistant message for transcript-backed drivers. History remains
// authoritative for final messages. Readers replace, rather than append,
// snapshots, so reconnecting cannot duplicate deltas.
type Content struct {
	ProtocolVersion int    `json:"protocol_version"`
	RuntimeID       string `json:"runtime_id"`
	WorkItemID      string `json:"work_item_id"`
	Sequence        uint64 `json:"sequence"`
	IdleGuard       bool   `json:"idle_guard"`
	MessageID       string `json:"message_id,omitempty"`
	Text            string `json:"text"`
	Truncated       bool   `json:"truncated,omitempty"`
	Activity        string `json:"activity"`
	LeafID          string `json:"leaf_id,omitempty"`
	// SavedAs lists the native history entries that hold Text, once the
	// driver knows them. Readers stop showing Text once history has one.
	SavedAs []string `json:"saved_as,omitempty"`
	// Thought is the current message's reasoning, when the agent shares it.
	Thought string       `json:"thought,omitempty"`
	Error   string       `json:"error,omitempty"`
	Tool    *ContentTool `json:"tool,omitempty"`
	// Tools are the current message's tool calls in order, Tool the latest.
	Tools []ContentTool `json:"tools,omitempty"`
	// Prompts link recently submitted prompts, by the request ID their sender
	// gave, to the native history entries holding them; newest last.
	Prompts []PromptLink `json:"prompts,omitempty"`
}

// PromptLink names the history entries that hold one submitted prompt.
type PromptLink struct {
	RequestID string   `json:"request_id"`
	NativeIDs []string `json:"native_ids"`
}
type ContentTool struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Input  string `json:"input,omitempty"`
	Output string `json:"output,omitempty"`
}

// ContentRequest asks for one snapshot, or with Stream for a snapshot followed
// by every later content event on the same connection.
type ContentRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	RuntimeID       string `json:"runtime_id"`
	WorkItemID      string `json:"work_item_id"`
	Stream          bool   `json:"stream,omitempty"`
}

// Content stream event types. They follow ACP v2 where it has an equivalent:
// a delta appends to the current message, a replacement sets its whole text,
// and tool events carry the tool's full current state.
const (
	ContentSnapshot         = "snapshot"
	ContentMessageStarted   = "message.started"
	ContentMessageDelta     = "message.delta"
	ContentMessageReplaced  = "message.replaced"
	ContentMessageCompleted = "message.completed"
	ContentMessageSaved     = "message.saved"
	ContentToolStarted      = "tool.started"
	ContentToolUpdated      = "tool.updated"
	ContentToolCompleted    = "tool.completed"
	ContentToolCleared      = "tool.cleared"
	ContentThoughtDelta     = "thought.delta"
	ContentPromptSaved      = "prompt.saved"
	ContentActivity         = "activity"
	ContentTurnSettled      = "turn.settled"
)

// ContentEvent is one change to a runtime's live content. Sequences increase
// by one per event; applying events in order to a snapshot yields the next
// snapshot (see ApplyContent).
type ContentEvent struct {
	Sequence  uint64       `json:"sequence"`
	Type      string       `json:"type"`
	MessageID string       `json:"message_id,omitempty"`
	Text      string       `json:"text,omitempty"`
	Truncated bool         `json:"truncated,omitempty"`
	Tool      *ContentTool `json:"tool,omitempty"`
	// NativeIDs are the history entries holding the current text
	// (message.saved), or a prompt (prompt.saved).
	NativeIDs []string `json:"native_ids,omitempty"`
	// RequestID is the sender's identity of a prompt (prompt.saved).
	RequestID string `json:"request_id,omitempty"`
	// Activity is the activity after the event. On an activity event it and
	// LeafID are set as given, even when empty; elsewhere empty keeps the
	// current activity.
	Activity string   `json:"activity,omitempty"`
	LeafID   string   `json:"leaf_id,omitempty"`
	Snapshot *Content `json:"snapshot,omitempty"`
}

// ApplyContent returns the snapshot after one event. Servers compute their own
// snapshots with it, so subscribers applying the same events stay identical.
func ApplyContent(value Content, event ContentEvent) Content {
	if event.Type == ContentSnapshot && event.Snapshot != nil {
		value = *event.Snapshot
		value.Tool = cloneTool(value.Tool)
		value.SavedAs = append([]string(nil), value.SavedAs...)
		value.Tools = append([]ContentTool(nil), value.Tools...)
		value.Prompts = append([]PromptLink(nil), value.Prompts...)
		return value
	}
	value.Sequence = event.Sequence
	value.Tool = cloneTool(value.Tool)
	switch event.Type {
	case ContentMessageStarted:
		value.MessageID, value.Text, value.Truncated, value.Tool, value.SavedAs, value.Thought, value.Tools = event.MessageID, "", false, nil, nil, "", nil
	case ContentMessageDelta:
		if !value.Truncated {
			value.Text, value.Truncated = boundedContentText(value.Text + event.Text)
			value.SavedAs = nil
		}
	case ContentMessageReplaced:
		value.Text, value.Truncated = boundedContentText(event.Text)
		value.Truncated = value.Truncated || event.Truncated
		value.SavedAs = nil
		if event.MessageID != "" {
			value.MessageID = event.MessageID
		}
	case ContentMessageSaved:
		value.SavedAs = append([]string(nil), event.NativeIDs...)
	case ContentToolStarted, ContentToolUpdated, ContentToolCompleted:
		value.Tool = cloneTool(event.Tool)
		if event.Tool != nil {
			value.Tools = upsertTool(value.Tools, *event.Tool)
		}
	case ContentToolCleared:
		value.Tool, value.Tools = nil, nil
	case ContentThoughtDelta:
		value.Thought = boundedThought(value.Thought + event.Text)
	case ContentPromptSaved:
		value.Prompts = linkPrompt(value.Prompts, PromptLink{RequestID: event.RequestID, NativeIDs: append([]string(nil), event.NativeIDs...)})
	case ContentActivity:
		value.Activity, value.LeafID = event.Activity, event.LeafID
		return value
	}
	if event.Activity != "" {
		value.Activity = event.Activity
	}
	return value
}

// maxContentTools bounds the tool calls kept for one message; the oldest go.
const maxContentTools = 32

// upsertTool replaces the call with the same id, or appends a new one. Calls
// without an id replace the last call of the same name.
func upsertTool(tools []ContentTool, tool ContentTool) []ContentTool {
	next := append([]ContentTool(nil), tools...)
	for i := len(next) - 1; i >= 0; i-- {
		if tool.ID != "" && next[i].ID == tool.ID || tool.ID == "" && next[i].ID == "" && next[i].Name == tool.Name {
			next[i] = tool
			return next
		}
	}
	next = append(next, tool)
	if len(next) > maxContentTools {
		next = next[len(next)-maxContentTools:]
	}
	return next
}

// maxPromptLinks bounds the prompt links a snapshot keeps; senders need only
// the few prompts still waiting for history.
const maxPromptLinks = 8

// linkPrompt records a prompt's entries, replacing an earlier link for the
// same request and dropping the oldest beyond the bound.
func linkPrompt(links []PromptLink, link PromptLink) []PromptLink {
	next := make([]PromptLink, 0, len(links)+1)
	for _, other := range links {
		if other.RequestID != link.RequestID {
			next = append(next, other)
		}
	}
	next = append(next, link)
	if len(next) > maxPromptLinks {
		next = next[len(next)-maxPromptLinks:]
	}
	return next
}

// boundedThought keeps the newest reasoning within the field limit.
func boundedThought(text string) string {
	const limit = 64 << 10
	if len(text) <= limit {
		return text
	}
	start := len(text) - limit
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}

func cloneTool(tool *ContentTool) *ContentTool {
	if tool == nil {
		return nil
	}
	copy := *tool
	return &copy
}

func boundedContentText(text string) (string, bool) {
	if len(text) <= MaxContentTextBytes {
		return text, false
	}
	end := MaxContentTextBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end], true
}

// ContentSocketPath is the optional v1 content endpoint for a runtime. Older
// drivers may not provide it. Runtime-specific paths avoid stale predecessor sockets.
// The filename has the same length as wi's bounded runtime control filenames.
func ContentSocketPath(control string) string {
	digest := sha256.Sum256([]byte("content:" + filepath.Base(control)))
	return filepath.Join(filepath.Dir(control), hex.EncodeToString(digest[:8])+".sock")
}

func ReadContent(ctx context.Context, path, runtimeID, itemID string) (Content, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return Content{}, err
	}
	defer conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if err := json.NewEncoder(conn).Encode(ContentRequest{ProtocolVersion: ContentVersion, RuntimeID: runtimeID, WorkItemID: itemID}); err != nil {
		return Content{}, err
	}
	var value Content
	err = json.NewDecoder(io.LimitReader(conn, 2<<20)).Decode(&value)
	if err != nil {
		return value, err
	}
	if value.Error != "" {
		return value, fmt.Errorf("%s", value.Error)
	}
	if value.ProtocolVersion != ContentVersion || value.RuntimeID != runtimeID || value.WorkItemID != itemID {
		return Content{}, fmt.Errorf("content owner mismatch")
	}
	return value, nil
}

// ErrContentStreamUnsupported means the runtime answered with a bare snapshot:
// its driver predates content streams, and readers should poll instead.
var ErrContentStreamUnsupported = errors.New("live content stream unsupported by this runtime")

// SubscribeContent streams a runtime's content: first a snapshot event, then
// every later event, each passed to apply. It returns when the context ends,
// the runtime closes the stream, apply fails, or an event is missing; callers
// subscribe again to start from a fresh snapshot.
func SubscribeContent(ctx context.Context, path, runtimeID, itemID string, apply func(ContentEvent) error) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if err := json.NewEncoder(conn).Encode(ContentRequest{ProtocolVersion: ContentVersion, RuntimeID: runtimeID, WorkItemID: itemID, Stream: true}); err != nil {
		return err
	}
	lines := bufio.NewScanner(conn)
	lines.Buffer(make([]byte, 64<<10), 4<<20)
	if !lines.Scan() {
		return streamEnd(ctx, lines.Err())
	}
	var first struct {
		ContentEvent
		Error string `json:"error"`
	}
	if err := json.Unmarshal(lines.Bytes(), &first); err != nil {
		return err
	}
	if first.Error != "" {
		return fmt.Errorf("%s", first.Error)
	}
	if first.Type != ContentSnapshot || first.Snapshot == nil {
		return ErrContentStreamUnsupported
	}
	if snapshot := first.Snapshot; snapshot.ProtocolVersion != ContentVersion || snapshot.RuntimeID != runtimeID || snapshot.WorkItemID != itemID {
		return fmt.Errorf("content owner mismatch")
	}
	_ = conn.SetDeadline(time.Time{})
	if err := apply(first.ContentEvent); err != nil {
		return err
	}
	last := first.Sequence
	for lines.Scan() {
		var event ContentEvent
		if err := json.Unmarshal(lines.Bytes(), &event); err != nil {
			return err
		}
		if event.Sequence != last+1 {
			return fmt.Errorf("content stream skipped from %d to %d", last, event.Sequence)
		}
		last = event.Sequence
		if err := apply(event); err != nil {
			return err
		}
	}
	return streamEnd(ctx, lines.Err())
}

func streamEnd(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	return io.EOF
}

// Content stream limits: a subscriber whose buffer fills is disconnected and
// must subscribe again, so a slow reader never holds up the driver.
const (
	maxContentSubscribers   = 32
	contentSubscriberBuffer = 512
	// Tool updates carry whole outputs, so a reader's backlog is bounded in
	// bytes too.
	contentSubscriberBytes = 8 << 20
)

// contentSubscriber is one stream reader's queue and its size in bytes.
type contentSubscriber struct {
	events chan ContentEvent
	queued atomic.Int64
}

func contentEventSize(event ContentEvent) int64 {
	size := len(event.Text)
	if event.Tool != nil {
		size += len(event.Tool.Input) + len(event.Tool.Output)
	}
	return int64(size)
}

type ContentServer struct {
	listener    net.Listener
	mu          sync.Mutex
	value       Content
	done        chan struct{}
	closed      bool
	subscribers map[*contentSubscriber]struct{}
}

func ListenContent(path, runtimeID, itemID string, idleGuard bool) (*ContentServer, error) {
	// Do not unlink an existing endpoint, which may belong to an active owner.
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = l.Close()
		return nil, err
	}
	s := &ContentServer{listener: l, value: Content{ProtocolVersion: ContentVersion, RuntimeID: runtimeID, WorkItemID: itemID, IdleGuard: idleGuard}, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s, nil
}
func (s *ContentServer) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	var req ContentRequest
	if json.NewDecoder(io.LimitReader(conn, 4096)).Decode(&req) != nil {
		return
	}
	value := s.snapshot()
	if req.ProtocolVersion != ContentVersion || req.RuntimeID != value.RuntimeID || req.WorkItemID != value.WorkItemID {
		_ = json.NewEncoder(conn).Encode(Content{Error: "content owner mismatch"})
		return
	}
	if !req.Stream {
		_ = json.NewEncoder(conn).Encode(value)
		return
	}
	s.stream(conn)
}

// stream sends a snapshot and then every event until the subscriber falls
// behind, disconnects, or the server closes.
func (s *ContentServer) stream(conn net.Conn) {
	reader, snapshot, err := s.subscribe()
	if err != nil {
		_ = json.NewEncoder(conn).Encode(Content{Error: err.Error()})
		return
	}
	defer s.unsubscribe(reader)
	// The reader only notices the subscriber hanging up.
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		_, _ = io.Copy(io.Discard, conn)
	}()
	_ = conn.SetDeadline(time.Time{})
	encoder := json.NewEncoder(conn)
	send := func(event ContentEvent) bool {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return encoder.Encode(event) == nil
	}
	if !send(ContentEvent{Sequence: snapshot.Sequence, Type: ContentSnapshot, Snapshot: &snapshot}) {
		return
	}
	for {
		select {
		case event, open := <-reader.events:
			if !open || !send(event) {
				return
			}
			reader.queued.Add(-contentEventSize(event))
		case <-gone:
			return
		}
	}
}

func (s *ContentServer) subscribe() (*contentSubscriber, Content, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, Content{}, fmt.Errorf("content server closed")
	}
	if len(s.subscribers) >= maxContentSubscribers {
		return nil, Content{}, fmt.Errorf("too many content subscribers")
	}
	if s.subscribers == nil {
		s.subscribers = map[*contentSubscriber]struct{}{}
	}
	reader := &contentSubscriber{events: make(chan ContentEvent, contentSubscriberBuffer)}
	s.subscribers[reader] = struct{}{}
	value := s.value
	value.Tool = cloneTool(value.Tool)
	return reader, value, nil
}

func (s *ContentServer) unsubscribe(reader *contentSubscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drop(reader)
}

// drop ends one reader's stream. The caller holds the lock.
func (s *ContentServer) drop(reader *contentSubscriber) {
	if _, ok := s.subscribers[reader]; ok {
		delete(s.subscribers, reader)
		close(reader.events)
	}
}

// Snapshot returns the current content, as a new reader would receive it.
func (s *ContentServer) Snapshot() Content { return s.snapshot() }

// snapshot owns its tool value so encoding cannot race the next update.
func (s *ContentServer) snapshot() Content {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.value
	value.Tool = cloneTool(value.Tool)
	return value
}

// publish numbers one event, applies it to the snapshot and sends it to every
// subscriber. The caller holds the lock.
func (s *ContentServer) publish(event ContentEvent) {
	event.Sequence = s.value.Sequence + 1
	event.Tool = cloneTool(event.Tool)
	s.value = ApplyContent(s.value, event)
	size := contentEventSize(event)
	for reader := range s.subscribers {
		if reader.queued.Load()+size > contentSubscriberBytes {
			s.drop(reader)
			continue
		}
		reader.queued.Add(size)
		select {
		case reader.events <- event:
		default:
			s.drop(reader)
		}
	}
}

func (s *ContentServer) Update(eventType, text, tool string) {
	s.UpdateTool(eventType, text, "", tool, "", "", false)
}

// UpdateTool turns one driver runtime event into content events.
func (s *ContentServer) UpdateTool(eventType, text, toolID, toolName, input, output string, failed bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Updates build on the same call's state; calls can run side by side.
	var prior *ContentTool
	for i := range s.value.Tools {
		if tool := &s.value.Tools[i]; tool.ID == toolID && (toolID != "" || tool.Name == toolName) {
			prior = tool
		}
	}
	switch eventType {
	case "message.started":
		s.publish(ContentEvent{Type: ContentMessageStarted, MessageID: fmt.Sprintf("live-%d", s.value.Sequence+1), Activity: "Assistant responding"})
	case EventMessageDelta:
		if !s.value.Truncated && text != "" {
			s.publish(ContentEvent{Type: ContentMessageDelta, MessageID: s.value.MessageID, Text: text, Activity: "Assistant responding"})
		}
	case EventMessageCompleted:
		s.publish(ContentEvent{Type: ContentMessageCompleted, MessageID: s.value.MessageID, Activity: "Message saved"})
	case EventToolStarted:
		s.publish(ContentEvent{Type: ContentToolStarted, Tool: &ContentTool{ID: toolID, Name: toolName, Status: "running", Input: boundedContentField(input)}, Activity: "Tool running: " + toolName})
	case EventToolUpdated:
		tool := &ContentTool{ID: toolID, Name: toolName, Status: "running"}
		if prior != nil {
			tool = cloneTool(prior)
		}
		tool.Output = boundedContentField(output)
		s.publish(ContentEvent{Type: ContentToolUpdated, Tool: tool, Activity: "Tool running: " + toolName})
	case EventToolCompleted:
		status := "completed"
		if failed {
			status = "failed"
		}
		tool := &ContentTool{ID: toolID, Name: toolName}
		if prior != nil {
			tool = cloneTool(prior)
		}
		tool.Status, tool.Output = status, boundedContentField(output)
		s.publish(ContentEvent{Type: ContentToolCompleted, Tool: tool, Activity: "Tool " + status + ": " + toolName})
	case EventAgentSettled:
		s.publish(ContentEvent{Type: ContentTurnSettled, Activity: "Idle"})
	}
}

// StartMessage begins a new message for drivers that otherwise publish whole
// snapshots, so its text, reasoning and tool calls start empty.
func (s *ContentServer) StartMessage() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publish(ContentEvent{Type: ContentMessageStarted, MessageID: fmt.Sprintf("live-%d", s.value.Sequence+1)})
}

// SetActivity changes what the agent is doing, keeping the branch leaf.
func (s *ContentServer) SetActivity(activity string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if activity = boundedContentField(activity); activity != s.value.Activity {
		s.publish(ContentEvent{Type: ContentActivity, Activity: activity, LeafID: s.value.LeafID})
	}
}

// SetBranch reports the branch tip the conversation now follows, with a new
// activity unless it is empty, without touching the live text: history
// readers follow the tip, and the reply shown stays until history has it.
func (s *ContentServer) SetBranch(leaf, activity string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if activity = boundedContentField(activity); activity == "" {
		activity = s.value.Activity
	}
	if leaf != s.value.LeafID || activity != s.value.Activity {
		s.publish(ContentEvent{Type: ContentActivity, Activity: activity, LeafID: leaf})
	}
}

// PromptSaved links a submitted prompt, by its sender's request ID, to the
// native history entries holding it. Prompts without a request ID are not
// linked: nobody waits for them.
func (s *ContentServer) PromptSaved(requestID string, nativeIDs []string) {
	if s == nil || requestID == "" || len(requestID) > 200 || !validNativeIDs(nativeIDs) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, link := range s.value.Prompts {
		if link.RequestID == requestID && reflect.DeepEqual(link.NativeIDs, nativeIDs) {
			return
		}
	}
	s.publish(ContentEvent{Type: ContentPromptSaved, RequestID: requestID, NativeIDs: nativeIDs})
}

// Thought appends shared reasoning to the current message.
func (s *ContentServer) Thought(text string) {
	if s == nil || text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publish(ContentEvent{Type: ContentThoughtDelta, MessageID: s.value.MessageID, Text: text})
}

// MarkSaved records the native history entries that now hold text. It is
// published only while the live text is that text, so a late or mismatched
// report cannot attach entries to a different message.
func (s *ContentServer) MarkSaved(text string, nativeIDs []string) {
	if s == nil || !validNativeIDs(nativeIDs) {
		return
	}
	text, _ = boundedContentText(text)
	s.mu.Lock()
	defer s.mu.Unlock()
	// Whitespace may differ: drivers join a message's text blocks with line
	// breaks that the streamed text did not have.
	live := strings.Join(strings.Fields(s.value.Text), " ")
	if live == "" || live != strings.Join(strings.Fields(text), " ") || reflect.DeepEqual(s.value.SavedAs, nativeIDs) {
		return
	}
	s.publish(ContentEvent{Type: ContentMessageSaved, MessageID: s.value.MessageID, NativeIDs: nativeIDs})
}

// validNativeIDs bounds the history entry IDs a driver reports.
func validNativeIDs(ids []string) bool {
	if len(ids) == 0 || len(ids) > 16 {
		return false
	}
	for _, id := range ids {
		if id == "" || len(id) > 128 {
			return false
		}
	}
	return true
}

// PublishTranscript replaces a transcript-backed snapshot. It cannot affect
// runtime events, durable status, or control acceptance.
func (s *ContentServer) PublishTranscript(leaf, text, activity string, tool *ContentTool) {
	s.publishSnapshot(leaf, text, activity, tool, false)
}

// PublishStreaming replaces private incremental content without emitting runtime
// events. Truncation may have occurred upstream before text reached this socket.
func (s *ContentServer) PublishStreaming(leaf, text, activity string, tool *ContentTool, truncated bool) {
	s.publishSnapshot(leaf, text, activity, tool, truncated)
}

// publishSnapshot turns a whole-snapshot update into the events that change
// the current snapshot into it: text that extends the current message is a
// delta, any other text a replacement.
func (s *ContentServer) publishSnapshot(leaf, text, activity string, tool *ContentTool, truncated bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	activity = boundedContentField(activity)
	text, cut := boundedContentText(text)
	truncated = truncated || cut
	if tool != nil {
		copy := *tool
		copy.Input = boundedContentField(copy.Input)
		copy.Output = boundedContentField(copy.Output)
		copy.Name = boundedContentField(copy.Name)
		tool = &copy
	}
	current := s.value
	if leaf != current.LeafID || activity != current.Activity {
		s.publish(ContentEvent{Type: ContentActivity, Activity: activity, LeafID: leaf})
	}
	if text != current.Text || truncated != current.Truncated {
		if !truncated && !current.Truncated && strings.HasPrefix(text, current.Text) {
			s.publish(ContentEvent{Type: ContentMessageDelta, MessageID: current.MessageID, Text: text[len(current.Text):]})
		} else {
			s.publish(ContentEvent{Type: ContentMessageReplaced, MessageID: current.MessageID, Text: text, Truncated: truncated})
		}
	}
	switch {
	case tool == nil && current.Tool != nil:
		s.publish(ContentEvent{Type: ContentToolCleared})
	case tool != nil && (current.Tool == nil || *tool != *current.Tool):
		kind := ContentToolUpdated
		if current.Tool == nil || current.Tool.ID != tool.ID {
			kind = ContentToolStarted
		}
		if tool.Status == "completed" || tool.Status == "failed" {
			kind = ContentToolCompleted
		}
		s.publish(ContentEvent{Type: kind, Tool: tool})
	}
}

func boundedContentField(value string) string {
	if len(value) <= 64<<10 {
		return value
	}
	end := 64 << 10
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "\n[content truncated]"
}

// Close stops accepting readers and ends every stream.
func (s *ContentServer) Close() error {
	err := s.listener.Close()
	<-s.done
	s.mu.Lock()
	s.closed = true
	for reader := range s.subscribers {
		s.drop(reader)
	}
	s.mu.Unlock()
	return err
}
