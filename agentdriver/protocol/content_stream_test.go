package driverprotocol

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// follow subscribes and applies every event to a local snapshot, sending each
// result on the returned channel.
func follow(t *testing.T, ctx context.Context, path string) (<-chan Content, <-chan error) {
	t.Helper()
	values := make(chan Content, 1024)
	done := make(chan error, 1)
	go func() {
		var value Content
		done <- SubscribeContent(ctx, path, "runtime", "item", func(event ContentEvent) error {
			value = ApplyContent(value, event)
			values <- value
			return nil
		})
	}()
	return values, done
}

// settle waits for the subscriber to reach a snapshot sequence.
func settle(t *testing.T, values <-chan Content, sequence uint64) Content {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case value := <-values:
			if value.Sequence == sequence {
				return value
			}
		case <-deadline:
			t.Fatalf("subscriber never reached sequence %d", sequence)
		}
	}
}

func TestContentStreamReproducesTheServerSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.sock")
	server, err := ListenContent(path, "runtime", "item", true)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.Update("message.started", "", "")
	server.Update(EventMessageDelta, "before ", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	values, _ := follow(t, ctx, path)
	if first := settle(t, values, server.snapshot().Sequence); first.Text != "before " {
		t.Fatalf("snapshot = %+v", first)
	}
	server.Update(EventMessageDelta, "subscribing", "")
	server.UpdateTool(EventToolStarted, "", "t1", "bash", `{"command":"ls"}`, "", false)
	server.UpdateTool(EventToolUpdated, "", "t1", "bash", "", "partial", false)
	server.UpdateTool(EventToolCompleted, "", "t1", "bash", "", "done", true)
	server.Update(EventMessageCompleted, "", "")
	// Whole snapshots from transcript-backed drivers become deltas or replacements.
	server.PublishStreaming("leaf-1", "Hel", "Streaming", nil, false)
	server.PublishStreaming("leaf-1", "Hello", "Streaming", &ContentTool{ID: "t2", Name: "read", Status: "running"}, false)
	server.PublishTranscript("leaf-2", "Goodbye", "Message saved", nil)
	server.Update(EventAgentSettled, "", "")
	want := server.snapshot()
	if got := settle(t, values, want.Sequence); !reflect.DeepEqual(got, want) {
		t.Fatalf("applied events = %+v\nserver snapshot = %+v", got, want)
	}
}

func TestContentStreamEventsAreMinimal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.sock")
	server, err := ListenContent(path, "runtime", "item", true)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan ContentEvent, 64)
	go func() {
		_ = SubscribeContent(ctx, path, "runtime", "item", func(event ContentEvent) error { events <- event; return nil })
	}()
	next := func() ContentEvent {
		select {
		case event := <-events:
			return event
		case <-time.After(3 * time.Second):
			t.Fatal("no event")
			return ContentEvent{}
		}
	}
	if next().Type != ContentSnapshot {
		t.Fatal("stream did not start with a snapshot")
	}
	server.PublishStreaming("", "Hel", "Streaming", nil, false)
	if event := next(); event.Type != ContentActivity || event.Activity != "Streaming" {
		t.Fatalf("activity = %+v", event)
	}
	if event := next(); event.Type != ContentMessageDelta || event.Text != "Hel" {
		t.Fatalf("first text = %+v", event)
	}
	server.PublishStreaming("", "Hello", "Streaming", nil, false)
	if event := next(); event.Type != ContentMessageDelta || event.Text != "lo" {
		t.Fatalf("extension = %+v", event)
	}
	server.PublishStreaming("", "Other", "Streaming", nil, false)
	if event := next(); event.Type != ContentMessageReplaced || event.Text != "Other" {
		t.Fatalf("rewrite = %+v", event)
	}
	// An unchanged snapshot sends nothing.
	server.PublishStreaming("", "Other", "Streaming", nil, false)
	server.Update(EventAgentSettled, "", "")
	if event := next(); event.Type != ContentTurnSettled {
		t.Fatalf("unchanged snapshot produced %+v", event)
	}
}

func TestSlowContentSubscribersAreDisconnected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.sock")
	server, err := ListenContent(path, "runtime", "item", true)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(ContentRequest{ProtocolVersion: ContentVersion, RuntimeID: "runtime", WorkItemID: "item", Stream: true}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for subscriberCount(server) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// Never read: publishing must not block, and the subscriber is dropped.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20*contentSubscriberBuffer; i++ {
			server.Update(EventMessageDelta, strings.Repeat("x", 64), "")
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled subscriber blocked the driver")
	}
	if n := subscriberCount(server); n != 0 {
		t.Fatalf("stalled subscriber kept: %d", n)
	}
}

func TestContentStreamDetectsSnapshotOnlyServers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request ContentRequest
		_ = json.NewDecoder(conn).Decode(&request)
		_ = json.NewEncoder(conn).Encode(Content{ProtocolVersion: ContentVersion, RuntimeID: "runtime", WorkItemID: "item", Text: "old"})
	}()
	err = SubscribeContent(context.Background(), path, "runtime", "item", func(ContentEvent) error { return nil })
	if !errors.Is(err, ErrContentStreamUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestClosingTheContentServerEndsStreams(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.sock")
	server, err := ListenContent(path, "runtime", "item", true)
	if err != nil {
		t.Fatal(err)
	}
	values, done := follow(t, context.Background(), path)
	settle(t, values, 0)
	_ = server.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stream ended without an error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream outlived its server")
	}
}

func subscriberCount(s *ContentServer) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subscribers)
}

func TestMarkSavedOnlyAttachesToTheMatchingText(t *testing.T) {
	s := &ContentServer{}
	s.Update("message.started", "", "")
	s.Update(EventMessageDelta, "The answer.\n", "")
	s.MarkSaved("A different message", []string{"wrong"})
	if s.snapshot().SavedAs != nil {
		t.Fatal("saved ids attached to different text")
	}
	s.Update(EventMessageCompleted, "", "")
	s.MarkSaved("The answer.", []string{"entry"})
	value := s.snapshot()
	if !reflect.DeepEqual(value.SavedAs, []string{"entry"}) || value.Text != "The answer.\n" {
		t.Fatalf("saved = %v text=%q", value.SavedAs, value.Text)
	}
	before := value.Sequence
	s.MarkSaved("The answer.", []string{"entry"})
	if s.snapshot().Sequence != before {
		t.Fatal("repeated report published again")
	}
	s.Update("message.started", "", "")
	if s.snapshot().SavedAs != nil {
		t.Fatal("a new message kept the previous saved ids")
	}
}

func TestSetActivityPublishesOnlyChanges(t *testing.T) {
	s := &ContentServer{}
	s.PublishStreaming("leaf", "", "Working", nil, false)
	before := s.snapshot().Sequence
	s.SetActivity("Thinking")
	s.SetActivity("Thinking")
	value := s.snapshot()
	if value.Sequence != before+1 || value.Activity != "Thinking" || value.LeafID != "leaf" {
		t.Fatalf("snapshot = %+v (before %d)", value, before)
	}
}

func TestParallelToolUpdatesKeepEachCallsInput(t *testing.T) {
	s := &ContentServer{}
	s.UpdateTool(EventToolStarted, "", "a", "bash", "first", "", false)
	s.UpdateTool(EventToolStarted, "", "b", "read", "second", "", false)
	s.UpdateTool(EventToolCompleted, "", "a", "bash", "", "done", false)
	tools := s.Snapshot().Tools
	if len(tools) != 2 || tools[0].Input != "first" || tools[0].Status != "completed" || tools[1].Status != "running" {
		t.Fatalf("tools = %+v", tools)
	}
}

func TestLargeBacklogsDisconnectBeforeTheEventLimit(t *testing.T) {
	s := &ContentServer{}
	reader, _, err := s.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 60<<10)
	sent := 0
	for ; sent < contentSubscriberBuffer && subscriberCount(s) == 1; sent++ {
		s.UpdateTool(EventToolUpdated, "", "t", "bash", "", big, false)
	}
	if subscriberCount(s) != 0 || sent >= contentSubscriberBuffer {
		t.Fatalf("still subscribed after %d large updates", sent)
	}
	if _, open := <-reader.events; !open {
		t.Fatal("queued events were discarded instead of drained")
	}
}
