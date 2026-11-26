package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"

	"github.com/regb/workitem/internal/testutil"
)

// ownedPane is tmux's display-message answer for pane, owned by this
// process group and in no special mode.
func ownedPane(pane string) string { return fmt.Sprintf("%s|%d|0|0|0", pane, syscall.Getpgrp()) }

// fakeTmux answers tmuxInput's tmux calls for pane %7 and records them.
// react, when set, runs before each answer and may change the state.
type fakeTmux struct {
	meta, screen, payload string
	calls                 []string
	react                 func(args []string)
}

func newFakeTmux(screen string) *fakeTmux { return &fakeTmux{meta: ownedPane("%7"), screen: screen} }

func (f *fakeTmux) input() *tmuxInput {
	return &tmuxInput{pane: "%7", owner: syscall.Getpgrp(), run: f.run}
}

func (f *fakeTmux) run(_ context.Context, input io.Reader, args ...string) (string, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if input != nil {
		data, _ := io.ReadAll(input)
		f.payload = string(data)
	}
	if f.react != nil {
		f.react(args)
	}
	switch args[0] {
	case "display-message":
		return f.meta, nil
	case "capture-pane":
		return f.screen, nil
	}
	return "", nil
}

// echo is a react that shows pasted text in the editor and clears it on
// submission, as Claude does.
func (f *fakeTmux) echo(args []string) {
	switch args[0] {
	case "paste-buffer":
		f.screen = "───\n❯ " + strings.ReplaceAll(f.payload, "\n", "\n  ") + "\n───"
	case "send-keys":
		f.screen = "───\n❯\n───"
	}
}

// count reports the recorded calls of one tmux command.
func (f *fakeTmux) count(command string) int {
	n := 0
	for _, call := range f.calls {
		if strings.HasPrefix(call, command+" ") || call == command {
			n++
		}
	}
	return n
}

// pipeFrames returns the write end of a pipe and the JSON frames written to
// it, decoded as T. The channel closes once the writer is closed.
func pipeFrames[T any](t *testing.T) (*os.File, <-chan T) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close(); _ = r.Close() })
	frames := make(chan T, 64)
	go func() {
		defer close(frames)
		d := json.NewDecoder(r)
		for {
			var v T
			if d.Decode(&v) != nil {
				return
			}
			frames <- v
		}
	}()
	return w, frames
}

// listenContent serves live content for runtime "runtime" of "item" at
// socket, and returns the server and a reader of its current snapshot.
func listenContent(t *testing.T, socket string) (*protocol.ContentServer, func() protocol.Content) {
	t.Helper()
	server, err := protocol.ListenContent(socket, "runtime", "item", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server, func() protocol.Content {
		t.Helper()
		content, err := protocol.ReadContent(context.Background(), socket, "runtime", "item")
		if err != nil {
			t.Fatal(err)
		}
		return content
	}
}

// eventLog collects the compact events a runtime submits.
type eventLog struct {
	mu     sync.Mutex
	events []protocol.RuntimeEvent
	// blackhole keeps the endpoint reachable but stops acknowledging events.
	blackhole atomic.Bool
}

func captureEvents(t *testing.T, path string) *eventLog {
	t.Helper()
	server, err := protocol.ListenEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	log := &eventLog{}
	go func() {
		for request := range server.Requests() {
			log.mu.Lock()
			log.events = append(log.events, request.Event)
			log.mu.Unlock()
			if !log.blackhole.Load() {
				request.Respond(nil)
			}
		}
	}()
	return log
}

func (l *eventLog) snapshot() []protocol.RuntimeEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]protocol.RuntimeEvent(nil), l.events...)
}

// waitFor fails the test unless an event matching match arrives in time.
func (l *eventLog) waitFor(t *testing.T, what string, match func(protocol.RuntimeEvent) bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		for _, e := range l.snapshot() {
			if match(e) {
				return
			}
		}
	}
	t.Fatal("missing event:", what)
}

// assertPrivate fails if any captured event contains one of the texts.
func (l *eventLog) assertPrivate(t *testing.T, texts ...string) {
	t.Helper()
	for _, e := range l.snapshot() {
		raw, _ := json.Marshal(e)
		for _, text := range texts {
			if strings.Contains(string(raw), text) {
				t.Fatalf("native text %q entered compact events: %s", text, raw)
			}
		}
	}
}

// headlessRuntime is a headless runtime launched against the fake backend in
// TestClaudeHeadlessBackendProcess.
type headlessRuntime struct {
	control string
	ready   chan protocol.Ready
	done    chan error
}

// startFakeHeadless starts runtime "runtime" of "item" in dir, with its events
// sent to eventsPath(dir).
func startFakeHeadless(ctx context.Context, t *testing.T, dir, locator string, env map[string]string) headlessRuntime {
	t.Helper()
	backend := filepath.Join(dir, "backend")
	testutil.WriteScript(t, backend, "WI_CLAUDE_HEADLESS_TEST=1 exec "+quote(os.Args[0])+" -test.run=TestClaudeHeadlessBackendProcess -- \"$@\"")
	r := headlessRuntime{control: filepath.Join(dir, "control.sock"), ready: make(chan protocol.Ready, 1), done: make(chan error, 1)}
	request := protocol.LaunchRequest{Runtime: protocol.Runtime{ID: "runtime", WorkItemID: "item", Mode: protocol.ModeHeadless, CWD: dir, ControlSocketPath: r.control, EventSocketPath: eventsPath(dir)}, Conversation: protocol.Conversation{Locator: locator}, Environment: env}
	go func() {
		r.done <- launch(ctx, request, Config{Executable: backend}, func(ready protocol.Ready) error { r.ready <- ready; return nil })
	}()
	return r
}

// submit sends a control command to the runtime.
func (r headlessRuntime) submit(ctx context.Context, t *testing.T, id, kind, message string) {
	t.Helper()
	if err := protocol.SubmitControl(ctx, r.control, protocol.ControlCommand{ProtocolVersion: protocol.RuntimeVersion, RuntimeID: "runtime", WorkItemID: "item", ID: id, RequestID: "req-" + id, Type: kind, Message: message}); err != nil {
		t.Fatal(err)
	}
}

func eventsPath(dir string) string { return filepath.Join(dir, "events.sock") }
