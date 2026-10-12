package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/runtimepath"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

func TestAgentExecTUIDriverUsesPublicControlAndEventProtocol(t *testing.T) {
	t.Setenv("GO_WANT_WI_TUI_DRIVER", "1")
	t.Setenv("WI_DAEMON_SOCKET", "/private/daemon.sock")
	t.Setenv("WI_DAEMON_PROTOCOL", "private")
	t.Setenv("WI_BUILD_IDENTITY", "private")
	root, runtimeDir := t.TempDir(), t.TempDir()
	st := store.New(root)
	if err := st.Ensure(); err != nil {
		t.Fatal(err)
	}
	itemID, conversationID, runtimeID := "01KZYHGDCECSFS4BJ2SNTQP49V", "01KZYHGDCECSFS4BJ2SNTQP49W", "tui-runtime"
	checkout := t.TempDir()
	manifest := model.NewManifest(itemID, "tui-driver", "TUI driver", nil, false, time.Now().UTC(), model.Repository{RootAtCreation: checkout}, model.Checkout{Specification: model.CheckoutSpecification{Branch: "wi/tui-driver"}, Claim: &model.WorkspaceClaim{ID: "test-claim", ExecutorProfile: "", Path: checkout}})
	manifest.State = model.StateWorking
	manifest.Agent = "test"
	manifest.PrimaryConversation = &model.Conversation{Driver: "test", ID: conversationID, Locator: "sessions/test/conversation.jsonl", Format: "test-jsonl"}
	if err := st.CreateItem(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	conversationPath := filepath.Join(st.ItemDir(itemID), filepath.FromSlash(manifest.PrimaryConversation.Locator))
	if err := os.MkdirAll(filepath.Dir(conversationPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conversationPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := model.AgentRuntime{ID: runtimeID, WorkItemID: itemID, Mode: string(agent.ModeTUI), State: model.AgentRuntimeStarting, ConversationID: conversationID, StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := st.SaveAgentRuntime(context.Background(), itemID, runtime); err != nil {
		t.Fatal(err)
	}

	db, err := coordinator.OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	manifestJSON, _ := json.Marshal(manifest)
	digest := sha256.Sum256(manifestJSON)
	if _, err := db.SyncImportedManifests(map[string]coordinator.ImportedManifest{itemID: {Manifest: manifest, Digest: hex.EncodeToString(digest[:])}}, true); err != nil {
		db.Close()
		t.Fatal(err)
	}
	ownerJSON, _ := json.Marshal(runtime)
	if err := db.UpdateProjections([]coordinator.ProjectionUpdate{{Projection: coordinator.RuntimeOwnershipProjection, Key: itemID, Value: ownerJSON}}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
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
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(context.Background()) }()
	client := &coordinator.Client{SocketPath: socket}
	t.Cleanup(func() { _ = client.Shutdown(context.Background()); <-serverDone })

	loaded, err := st.LoadManifest(itemID)
	if err != nil || loaded.PrimaryConversation == nil || loaded.PrimaryConversation.ID != conversationID {
		t.Fatalf("primary conversation before exec=%+v err=%v", loaded.PrimaryConversation, err)
	}
	application := app.New(st, nil)
	application.AgentsConfig = map[string]config.AgentDefinitionConfig{"test": {Driver: "test"}}
	application.DriversConfig = map[string]config.DriverDefinitionConfig{"test": {}}
	application.BundledDriverCommands["test"] = app.BundledDriverCommand{Path: os.Args[0], Args: []string{"-test.run=TestTUIDriverHelperProcess"}}
	application.DaemonSocketPath = socket
	application.AgentRuntimeSocketRoot = testutil.ShortTempDir(t)
	application.AgentRuntimeStateRoot = t.TempDir()
	done := make(chan error, 1)
	go func() {
		done <- application.AgentExec(context.Background(), itemID, conversationID, runtimeID, agent.ModeTUI)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("TUI driver exited before runtime.ready: %v", err)
		default:
		}
		result, err := client.RuntimeEvents(context.Background(), coordinator.RuntimeEventsRequest{ItemID: itemID, Limit: 20})
		if err == nil {
			for _, event := range result.Events {
				if event.Type == driverprotocol.EventRuntimeReady {
					goto ready
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("runtime.ready not relayed: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
ready:
	stored, err := st.LoadAgentRuntime(itemID)
	if err != nil || stored == nil || !stored.Capabilities.NativeTUI || !stored.Capabilities.Prompt {
		t.Fatalf("negotiated runtime=%+v err=%v", stored, err)
	}
	controlPath := applicationRuntimeControlPath(application.AgentRuntimeSocketRoot, itemID, runtimeID)
	shutdown := driverprotocol.ControlCommand{ProtocolVersion: driverprotocol.Version, ID: "shutdown-tui", RuntimeID: runtimeID, WorkItemID: itemID, Type: driverprotocol.CommandShutdown, CreatedAt: time.Now().UTC()}
	if err := driverprotocol.SubmitControl(context.Background(), controlPath, shutdown); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TUI driver did not stop")
	}
}

func applicationRuntimeControlPath(root, itemID, runtimeID string) string {
	return filepath.Join(root, filepath.FromSlash(runtimepath.ControlSocket(itemID, runtimeID)))
}

func TestTUIDriverHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_WI_TUI_DRIVER") != "1" {
		return
	}
	for _, key := range []string{"WI_DAEMON_SOCKET", "WI_DAEMON_PROTOCOL", "WI_BUILD_IDENTITY"} {
		if os.Getenv(key) != "" {
			t.Fatalf("driver received private daemon variable %s", key)
		}
	}
	request, reply, err := driverprotocol.ConnectRequest(context.Background(), os.Getenv(driverprotocol.BootstrapSocketEnv), driverprotocol.Hello{ProtocolVersion: driverprotocol.Version, Driver: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if request.Launch == nil || request.Launch.Runtime.Mode != driverprotocol.ModeTUI {
		t.Fatalf("request=%+v", request)
	}
	launch := request.Launch
	control, err := driverprotocol.ListenControl(launch.Runtime.ControlSocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	ready := driverprotocol.Ready{ProtocolVersion: driverprotocol.Version, Capabilities: driverprotocol.Capabilities{NativeTUI: true, Prompt: true, Cancel: true, Monitor: true}}
	if err := reply(driverprotocol.Response{ProtocolVersion: driverprotocol.Version, Ready: &ready}); err != nil {
		t.Fatal(err)
	}
	event := driverprotocol.RuntimeEvent{ProtocolVersion: driverprotocol.Version, EventID: "tui-ready", RuntimeID: launch.Runtime.ID, WorkItemID: launch.Runtime.WorkItemID, Type: driverprotocol.EventRuntimeReady, Timestamp: time.Now().UTC()}
	if err := driverprotocol.SubmitEvent(context.Background(), launch.Runtime.EventSocketPath, event); err != nil {
		t.Fatal(err)
	}
	command := <-control.Requests()
	if command.Command.Type != driverprotocol.CommandShutdown {
		t.Fatalf("command=%+v", command.Command)
	}
	command.Respond(nil)
}
