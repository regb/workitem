package primaryagent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/testutil"
)

func TestArchivedOwnerRefusesMessagesButAllowsEmergencyControls(t *testing.T) {
	root := testutil.ShortTempDir(t)
	runtime := &model.AgentRuntime{ID: "owner", WorkItemID: "item", Mode: "headless", HostPID: 42, HostProcessGroup: 42, HostStartTime: 42, Capabilities: model.RuntimeCapabilities{Prompt: true, Cancel: true, IdleGuard: true}}
	manifest := model.Manifest{ID: "item", State: model.StateArchived}
	st := &runtimeStore{itemDir: root, runtime: runtime}
	service := New(st, process(true), nil, nil, func(context.Context, contract.ResolveOptions) (model.Manifest, error) { return manifest, nil }, func() (string, error) { return "command", nil }, time.Now, nil)
	server, err := agent.ListenControlSocket(service.ControlSocketPath("item", runtime))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	opts := ControlOptions{CommandType: agent.CommandPrompt, Message: "work"}
	if _, err := service.Control(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("archived send %v", err)
	}
	seen := make(chan string, 2)
	go func() {
		for req := range server.Requests() {
			seen <- req.Command.Type
			req.Respond(nil)
		}
	}()
	for _, typ := range []string{agent.CommandAbort, agent.CommandShutdown} {
		opts.CommandType = typ
		opts.Message = ""
		if _, err := service.Control(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
		if actual := <-seen; actual != typ {
			t.Fatalf("command=%s", actual)
		}
	}
	manifest.State = model.StateWorking
	opts.CommandType = agent.CommandPrompt
	opts.Message = "work"
	opts.ExpectedRuntimeID = "superseded"
	if _, err := service.Control(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "owner changed") {
		t.Fatalf("stale owner %v", err)
	}
	opts.ExpectedRuntimeID = "owner"
	opts.RequireIdle = true
	runtime.Capabilities.IdleGuard = false
	if _, err := service.Control(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "idle-only") {
		t.Fatalf("unnegotiated idle-only control %v", err)
	}
	opts.RequireIdle = false
	opts.Images = []agent.ImageContent{{MIMEType: "image/png", Data: "aW1hZ2U="}}
	if _, err := service.Control(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "image input") {
		t.Fatalf("unnegotiated image input %v", err)
	}
}
