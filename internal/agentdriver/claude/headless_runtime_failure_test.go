package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

func TestHeadlessRuntimeFailurePaths(t *testing.T) {
	for _, tc := range []struct {
		scenario, reason string
		startup, fatal   bool
	}{
		{scenario: "missing-state", startup: true, fatal: true},
		{scenario: "old-version", startup: true, fatal: true},
		{scenario: "missing-capability", reason: "headless_protocol_error", fatal: true},
		{scenario: "missing-init", reason: "headless_protocol_error", fatal: true},
		{scenario: "blocked-hook", reason: "prompt_blocked"},
		{scenario: "authentication", reason: "native_auth_failed"},
		{scenario: "api-error", reason: "native_api_failed"},
		{scenario: "hold"},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			dir := t.TempDir()
			backend := filepath.Join(dir, "backend")
			if err := os.WriteFile(backend, []byte("#!/bin/sh\nWI_CLAUDE_HEADLESS_TEST=1 exec "+quote(os.Args[0])+" -test.run=TestClaudeHeadlessBackendProcess -- \"$@\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			control := filepath.Join(dir, "control.sock")
			events, err := protocol.ListenEvents(eventsPath(dir))
			if err != nil {
				t.Fatal(err)
			}
			defer events.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			var mu sync.Mutex
			var blackhole atomic.Bool
			captured := []protocol.RuntimeEvent{}
			go func() {
				for {
					select {
					case request := <-events.Requests():
						mu.Lock()
						captured = append(captured, request.Event)
						mu.Unlock()
						if !blackhole.Load() {
							request.Respond(nil)
						}
					case <-ctx.Done():
						return
					}
				}
			}()
			ready := make(chan protocol.Ready, 1)
			done := make(chan error, 1)
			env := map[string]string{"HOME": dir, "CLAUDE_CONFIG_DIR": filepath.Join(dir, ".claude"), "WI_CLAUDE_FAKE_SCENARIO": tc.scenario, "WI_CLAUDE_FAKE_IGNORE_TERM": fmt.Sprint(tc.scenario == "hold")}
			go func() {
				done <- launch(ctx, protocol.LaunchRequest{Runtime: protocol.Runtime{ID: "runtime", WorkItemID: "item", Mode: protocol.ModeHeadless, CWD: dir, ControlSocketPath: control, EventSocketPath: eventsPath(dir)}, Conversation: protocol.Conversation{Locator: filepath.Join(dir, "sessions", uuid("failure")+".jsonl")}, Environment: env}, Config{Executable: backend}, func(r protocol.Ready) error { ready <- r; return nil })
			}()
			// Even failed assertions must wait for owned-child cleanup.
			finished := false
			defer func() {
				cancel()
				if !finished {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("runtime did not clean up its child")
					}
				}
			}()
			select {
			case r := <-ready:
				if (r.Error != "") != tc.startup {
					t.Fatal(r)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			submit := func(id, kind, text string) {
				t.Helper()
				if err := protocol.SubmitControl(ctx, control, protocol.ControlCommand{ProtocolVersion: protocol.RuntimeVersion, RuntimeID: "runtime", WorkItemID: "item", ID: id, Type: kind, Message: text}); err != nil {
					t.Fatal(err)
				}
			}
			if !tc.startup {
				submit("input", protocol.CommandPrompt, "private input")
			}
			if !tc.fatal {
				found := false
				for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
					mu.Lock()
					for _, e := range captured {
						if e.CommandID == "input" && (tc.scenario == "hold" && e.Type == protocol.EventCommandAccepted || e.Type == protocol.EventAgentSettled && e.Failed && e.ReasonCode == tc.reason) {
							found = true
						}
					}
					mu.Unlock()
					if found {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !found {
					t.Fatal("missing correlated outcome", tc.reason)
				}
				if tc.scenario == "hold" {
					for i := 0; i < 31; i++ {
						submit(fmt.Sprintf("follow-%d", i), protocol.CommandFollowUp, "private queued input")
					}
					// Keep the endpoint reachable but stop acknowledging facts. This
					// is worse than an absent socket: every send hits its deadline.
					blackhole.Store(true)
				}
				start := time.Now()
				submit("stop", protocol.CommandShutdown, "")
				select {
				case err := <-done:
					finished = true
					if err != nil || time.Since(start) > 5*time.Second {
						t.Fatal("unbounded shutdown", err, time.Since(start))
					}
				case <-time.After(5 * time.Second):
					t.Fatal("shutdown blocked behind event delivery")
				}
			} else {
				select {
				case err := <-done:
					finished = true
					if err == nil {
						t.Fatal("incompatible native protocol accepted")
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			for _, socket := range []string{control, protocol.ContentSocketPath(control)} {
				if _, err := os.Lstat(socket); !os.IsNotExist(err) {
					t.Fatal("owned endpoint retained", socket, err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			for _, e := range captured {
				data, _ := json.Marshal(e)
				if strings.Contains(string(data), "private") {
					t.Fatal("native input or failure text entered compact events")
				}
				if tc.scenario != "hold" && e.Type == protocol.EventAgentStarted {
					t.Fatal("failure without model output reported model execution")
				}
			}
		})
	}
}
