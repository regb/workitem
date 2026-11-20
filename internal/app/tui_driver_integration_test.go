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
	manifest := model.NewManifest(itemID, "tui-driver", "TUI driver", nil, false, time.Now().UTC(), model.Repository{RootAtCreation: checkout}, model.Checkout{Specification: model.CheckoutSpecification{Branch: "wi/tui-driver"}, ClaimGeneration: 1, Claim: &model.WorkspaceClaim{ID: "test-claim", Generation: 1, Executor: model.ExecutorLocal, Path: checkout}})
	manifest.State = model.StateActive
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
	application.StaticAgentProfiles.Profiles = []model.AgentProfile{{Name: "test", Driver: "test"}}
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
		// The ready event and the negotiated runtime record are written
		// independently: the event is relayed while the host saves the
		// driver's ready reply. Wait for both.
		result, err := client.RuntimeEvents(context.Background(), coordinator.RuntimeEventsRequest{ItemID: itemID, Limit: 20})
		relayed := false
		if err == nil {
			for _, event := range result.Events {
				relayed = relayed || event.Type == driverprotocol.EventRuntimeReady
			}
		}
		stored, loadErr := st.LoadAgentRuntime(itemID)
		negotiated := loadErr == nil && stored != nil && stored.Capabilities.NativeTUI && stored.Capabilities.Prompt
		if relayed && negotiated {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runtime.ready relayed=%v; negotiated runtime=%+v err=%v %v", relayed, stored, err, loadErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	controlPath := applicationRuntimeControlPath(application.AgentRuntimeSocketRoot, itemID, runtimeID)
	shutdown := driverprotocol.ControlCommand{ProtocolVersion: driverprotocol.RuntimeVersion, ID: "shutdown-tui", RuntimeID: runtimeID, WorkItemID: itemID, Type: driverprotocol.CommandShutdown, CreatedAt: time.Now().UTC()}
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
	event := driverprotocol.RuntimeEvent{ProtocolVersion: driverprotocol.RuntimeVersion, EventID: "tui-ready", RuntimeID: launch.Runtime.ID, WorkItemID: launch.Runtime.WorkItemID, Type: driverprotocol.EventRuntimeReady, Timestamp: time.Now().UTC()}
	if err := driverprotocol.SubmitEvent(context.Background(), launch.Runtime.EventSocketPath, event); err != nil {
		t.Fatal(err)
	}
	command := <-control.Requests()
	if command.Command.Type != driverprotocol.CommandShutdown {
		t.Fatalf("command=%+v", command.Command)
	}
	command.Respond(nil)
}
