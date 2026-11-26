package claude

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"

	"github.com/regb/workitem/internal/testutil"
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
			dir := testutil.ShortTempDir(t)
			events := captureEvents(t, eventsPath(dir))
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			env := map[string]string{"HOME": dir, "CLAUDE_CONFIG_DIR": filepath.Join(dir, ".claude"), "WI_CLAUDE_FAKE_SCENARIO": tc.scenario, "WI_CLAUDE_FAKE_IGNORE_TERM": fmt.Sprint(tc.scenario == "hold")}
			runtime := startFakeHeadless(ctx, t, dir, filepath.Join(dir, "sessions", uuid("failure")+".jsonl"), env)
			// Even failed assertions must wait for owned-child cleanup.
			finished := false
			defer func() {
				cancel()
				if !finished {
					select {
					case <-runtime.done:
					case <-time.After(5 * time.Second):
						t.Error("runtime did not clean up its child")
					}
				}
			}()
			select {
			case r := <-runtime.ready:
				if (r.Error != "") != tc.startup {
					t.Fatal(r)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if !tc.startup {
				runtime.submit(ctx, t, "input", protocol.CommandPrompt, "private input")
			}
			if !tc.fatal {
				events.waitFor(t, "correlated outcome "+tc.reason, func(e protocol.RuntimeEvent) bool {
					return e.CommandID == "input" && (tc.scenario == "hold" && e.Type == protocol.EventCommandAccepted || e.Type == protocol.EventAgentSettled && e.Failed && e.ReasonCode == tc.reason)
				})
				if tc.scenario == "hold" {
					for i := 0; i < 31; i++ {
						runtime.submit(ctx, t, fmt.Sprintf("follow-%d", i), protocol.CommandFollowUp, "private queued input")
					}
					// Keep the endpoint reachable but stop acknowledging facts. This
					// is worse than an absent socket: every send hits its deadline.
					events.blackhole.Store(true)
				}
				start := time.Now()
				runtime.submit(ctx, t, "stop", protocol.CommandShutdown, "")
				select {
				case err := <-runtime.done:
					finished = true
					if err != nil || time.Since(start) > 5*time.Second {
						t.Fatal("unbounded shutdown", err, time.Since(start))
					}
				case <-time.After(5 * time.Second):
					t.Fatal("shutdown blocked behind event delivery")
				}
			} else {
				select {
				case err := <-runtime.done:
					finished = true
					if err == nil {
						t.Fatal("incompatible native protocol accepted")
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			for _, socket := range []string{runtime.control, protocol.ContentSocketPath(runtime.control)} {
				if _, err := os.Lstat(socket); !os.IsNotExist(err) {
					t.Fatal("owned endpoint retained", socket, err)
				}
			}
			events.assertPrivate(t, "private")
			for _, e := range events.snapshot() {
				if tc.scenario != "hold" && e.Type == protocol.EventAgentStarted {
					t.Fatal("failure without model output reported model execution")
				}
			}
		})
	}
}
