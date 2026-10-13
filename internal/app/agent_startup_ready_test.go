package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

func TestInitialPromptReadinessRequiresOriginalRuntimeOwner(t *testing.T) {
	for _, mode := range []string{"delayed", "timeout", "stopped", "superseded"} {
		t.Run(mode, func(t *testing.T) {
			native := store.New(t.TempDir())
			if err := native.Ensure(); err != nil {
				t.Fatal(err)
			}
			id, _ := model.NewID()
			m := model.NewManifest(id, "startup", "Startup", nil, false, time.Now(), model.Repository{}, model.NewCheckout(model.WorkspaceKindManagedSlot, model.ExecutorLocal, "task"))
			if err := native.CreateItem(context.Background(), m); err != nil {
				t.Fatal(err)
			}
			runtime := model.AgentRuntime{ID: "original", WorkItemID: id, Mode: "headless", State: model.AgentRuntimeStarting, Capabilities: model.RuntimeCapabilities{Prompt: true}}
			current := runtime
			if mode == "stopped" {
				current.State = model.AgentRuntimeStopped
			}
			if mode == "superseded" {
				current.ID = "replacement"
			}
			if err := native.SaveAgentRuntime(context.Background(), id, current); err != nil {
				t.Fatal(err)
			}
			a := &App{Store: native, AgentRuntimeSocketRoot: testutil.ShortTempDir(t), AgentRuntimeReadyTimeout: 50 * time.Millisecond, AgentRuntimeReadyInterval: time.Millisecond}
			socket := a.runtimeControlSocketPath(id, &runtime)
			if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
				t.Fatal(err)
			}
			if mode == "superseded" {
				if err := os.WriteFile(socket, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			ready := make(chan error, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			go func() { ready <- a.WaitForAgentRuntimeReady(ctx, id, &runtime) }()
			if mode == "delayed" {
				select {
				case err := <-ready:
					t.Fatalf("startup accepted before readiness: %v", err)
				case <-time.After(5 * time.Millisecond):
				}
				if err := os.WriteFile(socket, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := <-ready
			if mode == "delayed" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid startup reported ready")
			}
			if mode == "superseded" && !strings.Contains(err.Error(), "ownership changed") {
				t.Fatal(err)
			}
		})
	}
}
