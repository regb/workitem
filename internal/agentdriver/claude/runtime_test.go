package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/testutil"
)

func TestTUIRuntimeAndResume(t *testing.T) {
	dir := testutil.ShortTempDir(t)
	home := t.TempDir()
	cwd := t.TempDir()
	backend := filepath.Join(dir, "claude")
	script := "#!/bin/sh\nWI_CLAUDE_TEST_BACKEND=1 exec " + quote(os.Args[0]) + " -test.run=TestClaudeBackendHelperProcess -- \"$@\"\n"
	if err := os.WriteFile(backend, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(t.TempDir(), uuid("runtime")+".jsonl")
	if err := os.WriteFile(transcript, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for generation := 1; generation <= 6; generation++ {
		t.Run(fmt.Sprint(generation), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			control := filepath.Join(dir, "control.sock")
			eventPath := filepath.Join(dir, "events.sock")
			server, err := protocol.ListenEvents(eventPath)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			events := make(chan protocol.RuntimeEvent, 20)
			go func() {
				for r := range server.Requests() {
					r.Respond(nil)
					events <- r.Event
				}
			}()
			request := protocol.LaunchRequest{Runtime: protocol.Runtime{ID: fmt.Sprintf("runtime-%d", generation), WorkItemID: "item", Mode: protocol.ModeTUI, CWD: cwd, ControlSocketPath: control, EventSocketPath: eventPath}, Conversation: protocol.Conversation{ID: "conversation", Locator: transcript}, Environment: map[string]string{"HOME": home, "CLAUDE_CONFIG_DIR": "", "WI_CLAUDE_EXPECT_RESUME": fmt.Sprint(generation > 1), "WI_CLAUDE_TEST_SCENARIO": fmt.Sprint(generation)}}
			ready := make(chan protocol.Ready, 1)
			done := make(chan error, 1)
			go func() {
				done <- launch(ctx, request, Config{Executable: backend}, func(r protocol.Ready) error { ready <- r; return nil })
			}()
			select {
			case r := <-ready:
				if r.Error != "" || !r.Capabilities.NativeTUI || !r.Capabilities.LiveContent || r.Capabilities.Prompt || r.Capabilities.Headless {
					t.Fatalf("capabilities: %+v", r)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			expected := []string{protocol.EventRuntimeReady, protocol.EventAgentStarted, protocol.EventToolStarted, protocol.EventToolCompleted, protocol.EventAgentSettled}
			if generation == 5 {
				expected = nil
			}
			for _, kind := range expected {
			nextEvent:
				select {
				case event := <-events:
					if event.Type == protocol.EventRuntimeStatus {
						goto nextEvent
					}
					if event.Type != kind || event.RuntimeID != request.Runtime.ID || event.WorkItemID != "item" || (kind == protocol.EventAgentSettled && event.Failed != (generation == 3)) {
						t.Fatalf("event: %+v", event)
					}
				case err := <-done:
					t.Fatalf("early exit: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			command := protocol.ControlCommand{ProtocolVersion: protocol.RuntimeVersion, ID: "command", RuntimeID: request.Runtime.ID, WorkItemID: "item", Type: protocol.CommandPrompt, Message: "unsupported"}
			if err := protocol.SubmitControl(ctx, control, command); err == nil {
				t.Fatal("unsupported prompt accepted")
			}
			command.Type = protocol.CommandShutdown
			command.Message = ""
			command.RuntimeID = "stale"
			if err := protocol.SubmitControl(ctx, control, command); err == nil {
				t.Fatal("stale runtime accepted")
			}
			command.RuntimeID = request.Runtime.ID
			command.RequireIdle = true
			if generation == 5 {
				if err := protocol.SubmitControl(ctx, control, command); err == nil {
					t.Fatal("shutdown assumed idle before native readiness")
				}
				command.RequireIdle = false
				if err := protocol.SubmitControl(ctx, control, command); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				return
			}
			hookSocket := hookSocketPath(control)
			if err := RunHook(ctx, hookSocket, strings.NewReader(fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":%q}`, uuid("runtime")))); err != nil {
				t.Fatal(err)
			}
			if err := protocol.SubmitControl(ctx, control, command); err == nil {
				t.Fatal("idle-required shutdown accepted a busy runtime")
			}
			if err := RunHook(ctx, hookSocket, strings.NewReader(fmt.Sprintf(`{"hook_event_name":"Stop","session_id":%q}`, uuid("runtime")))); err != nil {
				t.Fatal(err)
			}
			if generation == 6 {
				_ = server.Close()
			}
			if err := protocol.SubmitControl(ctx, control, command); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
	if observed, warnings := observe(transcript, time.Now()); !observed.Exists || len(warnings) > 0 {
		t.Fatalf("%+v %v", observed, warnings)
	}
}
func TestClaudeBackendHelperProcess(t *testing.T) {
	if os.Getenv("WI_CLAUDE_TEST_BACKEND") != "1" {
		return
	}
	args := os.Args
	bypass := false
	for _, arg := range args {
		if arg == "--dangerously-skip-permissions" {
			bypass = true
		}
		if arg == "--permission-mode" {
			fmt.Fprintln(os.Stderr, "unexpected configurable permission mode")
			os.Exit(1)
		}
	}
	if !bypass {
		fmt.Fprintln(os.Stderr, "permission bypass missing on create/resume")
		os.Exit(1)
	}
	value := func(flag string) string {
		for i, a := range args {
			if a == flag && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	scenario := os.Getenv("WI_CLAUDE_TEST_SCENARIO")
	if scenario == "4" {
		signal.Ignore(syscall.SIGTERM)
	}
	id := value("--session-id")
	resume := value("--resume")
	if (resume != "") != (os.Getenv("WI_CLAUDE_EXPECT_RESUME") == "true") {
		fmt.Fprintln(os.Stderr, "wrong create/resume mode")
		os.Exit(1)
	}
	if id == "" {
		id = resume
	}
	if scenario == "5" {
		for {
			time.Sleep(time.Second)
		}
	}
	native := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", os.Getenv("CLAUDE_CODE_PROJECT_DIR_NAME"), id+".jsonl")
	if resume == "" {
		if _, err := os.Stat(native); err == nil {
			fmt.Fprintln(os.Stderr, "session ID already exists")
			os.Exit(1)
		}
	}
	file, err := os.OpenFile(native, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cwd, _ := os.Getwd()
	line := fmt.Sprintf(`{"type":"user","sessionId":%q,"cwd":%q,"timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"fake task"}}`, id, cwd)
	_, _ = fmt.Fprintln(file, line)
	_ = file.Close()
	var settings struct {
		Hooks map[string][]struct{ Hooks []struct{ Command string } }
	}
	data, err := os.ReadFile(value("--settings"))
	if err != nil {
		os.Exit(1)
	}
	if json.Unmarshal(data, &settings) != nil {
		os.Exit(1)
	}
	events := []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"}
	if scenario == "3" {
		events[len(events)-1] = "StopFailure"
	}
	for _, event := range events {
		input, _ := json.Marshal(map[string]any{"hook_event_name": event, "session_id": id, "transcript_path": native, "tool_name": "Read", "prompt": "DO_NOT_FORWARD_PROMPT", "tool_input": map[string]any{"file_path": "DO_NOT_FORWARD_PATH"}})
		cmd := exec.Command("sh", "-c", settings.Hooks[event][0].Hooks[0].Command)
		cmd.Stdin = strings.NewReader(string(input))
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "hook: %s %v", out, err)
			os.Exit(1)
		}
	}
	for {
		time.Sleep(time.Second)
	}
}
