package agent_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/agent"
)

func TestControlSocketReturnsRuntimeRejection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	server, err := agent.ListenControlSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go func() {
		request := <-server.Requests()
		request.Respond(context.Canceled)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := agent.SubmitControlSocket(ctx, path, agent.ControlCommand{Type: agent.CommandShutdown}); err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("err = %v", err)
	}
}

func TestControlSocketRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	server, err := agent.ListenControlSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go func() {
		request := <-server.Requests()
		if request.Command.Type != agent.CommandSteer || request.Command.Message != "hello" {
			request.Respond(context.Canceled)
			return
		}
		request.Respond(nil)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := agent.SubmitControlSocket(ctx, path, agent.ControlCommand{Type: agent.CommandSteer, Message: "hello"}); err != nil {
		t.Fatal(err)
	}
}
