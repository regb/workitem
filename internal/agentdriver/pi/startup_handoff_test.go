package pi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/testutil"
)

func TestNativeRPCStartupQueueOverflowAndInterruptedHandoff(t *testing.T) {
	for _, ending := range []string{"exit", "cancel"} {
		t.Run(ending, func(t *testing.T) {
			dir := testutil.ShortTempDir(t)
			fakePi := filepath.Join(dir, "fake-pi")
			// Read the readiness probe, but never respond. A marker controls exit.
			script := "#!/usr/bin/env bash\nset -euo pipefail\nIFS= read -r line\nwhile [ ! -f \"$1\" ]; do sleep 0.01; done\n"
			if err := os.WriteFile(fakePi, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			exitMarker := filepath.Join(dir, "exit")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			control := filepath.Join(dir, "control.sock")
			runtimeDone := make(chan error, 1)
			go func() {
				runtimeDone <- runNativeRPC(ctx, fakePi, []string{exitMarker}, os.Environ(), ExecSpec{CWD: dir, RuntimeID: "owner", WorkItemID: "item", ControlSocketPath: control, EventSocketPath: filepath.Join(dir, "events.sock")})
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(control); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("control socket was not created")
				}
				time.Sleep(time.Millisecond)
			}
			requestCtx, requestCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer requestCancel()
			results := make(chan error, 33)
			for i := 0; i < 33; i++ {
				go func(i int) {
					results <- agent.SubmitControlSocket(requestCtx, control, agent.ControlCommand{ProtocolVersion: agent.RuntimeProtocolVersion, ID: fmt.Sprintf("queued-%d", i), RuntimeID: "owner", WorkItemID: "item", Type: agent.CommandAbort})
				}(i)
			}
			// One refusal proves that the other 32 requests occupy the queue.
			select {
			case err := <-results:
				if err == nil || !strings.Contains(err.Error(), "startup queue is full") {
					t.Fatalf("overflow result: %v", err)
				}
			case <-requestCtx.Done():
				t.Fatal("startup queue did not reject overflow")
			}
			select {
			case err := <-results:
				t.Fatalf("queued command completed before interruption: %v", err)
			default:
			}
			if ending == "exit" {
				if err := os.WriteFile(exitMarker, nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			for i := 0; i < 32; i++ {
				select {
				case err := <-results:
					if err == nil || !strings.Contains(err.Error(), "stopped before becoming ready") {
						t.Fatalf("queued command lost its explicit refusal: %v", err)
					}
				case <-requestCtx.Done():
					t.Fatal("queued client remained blocked after interruption")
				}
			}
			select {
			case <-runtimeDone:
			case <-requestCtx.Done():
				t.Fatal("runtime remained blocked after interruption")
			}
		})
	}
}
