package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/xdg"
)

func TestDaemonStatusAndStopCommands(t *testing.T) {
	paths := xdg.Paths{DataRoot: t.TempDir(), RuntimeDir: t.TempDir()}
	socket, err := coordinator.SocketPath(paths.RuntimeDir, paths.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	server, err := coordinator.NewServer(paths.DataRoot, socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	var stdout, stderr bytes.Buffer
	if code := runDaemonMain(context.Background(), []string{"status"}, paths, &stdout, &stderr, false); code != ExitOK || !strings.Contains(stdout.String(), "daemon: running") {
		t.Fatalf("status code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runDaemonMain(context.Background(), []string{"stop"}, paths, &stdout, &stderr, false); code != ExitOK {
		t.Fatalf("stop code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
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
