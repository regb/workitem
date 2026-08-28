package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/store"
)

func TestShutdownWorkerEnvironmentRemovesRuntimeOwnership(t *testing.T) {
	filtered := shutdownWorkerEnvironment([]string{
		"HOME=/home/test",
		"XDG_DATA_HOME=/tmp/data",
		"TMUX=/tmp/tmux",
		"TMUX_PANE=%1",
		"WI_ID=item",
		"WI_AGENT_RUNTIME_ID=runtime",
		"PI_CODING_AGENT_SESSION_DIR=/tmp/session",
		"WI_LIST_LABELS=team",
	})
	joined := strings.Join(filtered, "\n")
	for _, forbidden := range []string{"WI_ID=", "WI_AGENT_RUNTIME_ID=", "PI_CODING_AGENT_SESSION_DIR="} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("filtered environment retained %q: %q", forbidden, joined)
		}
	}
	for _, wanted := range []string{"HOME=/home/test", "XDG_DATA_HOME=/tmp/data", "TMUX=/tmp/tmux", "TMUX_PANE=%1", "WI_LIST_LABELS=team"} {
		if !strings.Contains(joined, wanted) {
			t.Fatalf("filtered environment removed %q: %q", wanted, joined)
		}
	}
}

func TestShutdownOperationEnvironmentDoesNotLookAttached(t *testing.T) {
	filtered := shutdownOperationEnvironment(map[string]string{
		"HOME": "/home/test", "TMUX": "/tmp/tmux", "TMUX_PANE": "%1", "WI_ID": "item", "WI_AGENT_RUNTIME_ID": "runtime",
	})
	if filtered["HOME"] != "/home/test" {
		t.Fatalf("HOME = %q", filtered["HOME"])
	}
	for _, key := range []string{"TMUX", "TMUX_PANE", "WI_ID", "WI_AGENT_RUNTIME_ID"} {
		if _, ok := filtered[key]; ok {
			t.Fatalf("operation environment retained %s", key)
		}
	}
}

func TestShutdownFromTmuxSchedulesDetachedWorker(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("true executable is unavailable")
	}
	application := &app.App{SelfPath: truePath}
	var stdout, stderr bytes.Buffer
	cfg := Config{Stdout: &stdout, Stderr: &stderr, Env: map[string]string{"TMUX": "/tmp/tmux"}, App: application, StateRoot: t.TempDir()}
	if err := runShutdown(context.Background(), nil, cfg, true); err != nil {
		t.Fatal(err)
	}
	var result shutdownCommandResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode %q: %v", stdout.String(), err)
	}
	if !result.Scheduled || result.WorkerPID <= 0 || result.LogPath == "" {
		t.Fatalf("result = %+v", result)
	}
}

func TestShutdownWorkerStopsDaemon(t *testing.T) {
	root, runtimeDir := t.TempDir(), t.TempDir()
	st := store.New(root)
	if err := st.Ensure(); err != nil {
		t.Fatal(err)
	}
	socket, err := coordinator.SocketPath(runtimeDir, root)
	if err != nil {
		t.Fatal(err)
	}
	server, err := coordinator.NewServer(root, socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()

	application := app.New(st, nil)
	client := &coordinator.Client{SocketPath: socket}
	var stdout, stderr bytes.Buffer
	cfg := Config{Stdout: &stdout, Stderr: &stderr, Env: map[string]string{}, App: application, Coordinator: client, StateRoot: filepath.Join(t.TempDir(), "state")}
	if err := runShutdown(context.Background(), []string{"--worker"}, cfg, true); err != nil {
		t.Fatalf("shutdown: %v, stderr=%q", err, stderr.String())
	}
	var result shutdownCommandResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode %q: %v", stdout.String(), err)
	}
	if !result.DaemonStopped || len(result.Failures) != 0 {
		t.Fatalf("result = %+v", result)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not stop")
	}
}
