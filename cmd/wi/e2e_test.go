package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitransport"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/testutil"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type binaryFixture struct {
	root, wi, driver, source, socket, agentSocket string
	env                                           []string
	clients                                       *apitransport.Clients
	t                                             *testing.T
}

func newBinaryFixture(t *testing.T) *binaryFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("real-binary API smoke test")
	}
	root, err := os.MkdirTemp("/tmp", "wi-bin-api-")
	if err != nil {
		t.Fatal(err)
	}
	f := &binaryFixture{root: root, wi: filepath.Join(root, "wi"), driver: filepath.Join(root, "fake-driver"), source: filepath.Join(root, "source"), t: t}
	t.Cleanup(func() {
		if f.clients != nil {
			f.clients.Close()
		}
		cmd := exec.Command(f.wi, "daemon", "stop")
		cmd.Env = f.env
		cmd.Dir = root
		_ = cmd.Run()
		time.Sleep(150 * time.Millisecond)
		_ = os.RemoveAll(root)
	})
	_, file, _, _ := runtime.Caller(0)
	repository := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	for _, target := range []struct{ path, pkg string }{{f.wi, "./cmd/wi"}, {f.driver, "./internal/apitest/driver"}} {
		cmd := exec.Command("go", "build", "-o", target.path, target.pkg)
		cmd.Dir = repository
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v %s", target.pkg, err, out)
		}
	}
	f.env = testutil.IsolatedEnvironment(os.Environ(), root)
	for _, part := range []string{"home", "data", "config/wi", "cache", "state", "run", "pi", "tmux", "source"} {
		if err := os.MkdirAll(filepath.Join(root, part), 0700); err != nil {
			t.Fatal(err)
		}
	}
	config := "[drivers.pi]\ncommand = " + fmt.Sprintf("%q", f.driver) + "\n"
	if err := os.WriteFile(filepath.Join(root, "config", "wi", "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.name", "API smoke"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "-m", "initial"}} {
		cmd := exec.Command("git", append([]string{"-C", f.source}, args...)...)
		cmd.Env = f.env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	f.run("list", "--json") // Read-only commands also exercise isolated daemon autostart.
	f.run("daemon", "start")
	f.socket, _ = coordinator.SocketPath(filepath.Join(root, "run"), filepath.Join(root, "data", "wi"))
	f.agentSocket, _ = coordinator.AgentSocketPath(filepath.Join(root, "run"), filepath.Join(root, "data", "wi"))
	f.clients, err = apitransport.NewClients(f.socket)
	if err != nil {
		t.Fatal(err)
	}
	// Stop only runtimes created by this fixture. TMUX_TMPDIR is isolated too.
	t.Cleanup(func() {
		if f.clients == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		items, err := f.clients.Items.ListItems(ctx, connect.NewRequest(&v1.ListItemsRequest{States: []v1.ItemState{1, 2, 3}}))
		if err == nil {
			for _, item := range items.Msg.Items {
				_, _ = f.clients.Agents.StopAgent(ctx, connect.NewRequest(&v1.StopAgentRequest{ItemId: item.Id, Force: true, ConfirmAttachedTerminal: true}))
			}
		}
	})
	return f
}
func (f *binaryFixture) command(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.wi, args...)
	cmd.Env = f.env
	cmd.Dir = f.root
	return cmd.CombinedOutput()
}
func (f *binaryFixture) run(args ...string) []byte {
	f.t.Helper()
	out, err := f.command(args...)
	if err != nil {
		logs, _ := filepath.Glob(filepath.Join(f.root, "state", "wi", "*", "daemon.log"))
		for _, path := range logs {
			data, _ := os.ReadFile(path)
			f.t.Logf("daemon log: %s", data)
		}
		f.t.Fatalf("wi %v: %v\n%s", args, err, out)
	}
	return out
}
func (f *binaryFixture) json(msg proto.Message, args ...string) {
	f.t.Helper()
	out := f.run(append(args, "--json")...)
	if err := protojson.Unmarshal(out, msg); err != nil {
		f.t.Fatalf("wi %v API JSON: %v\n%s", args, err, out)
	}
}
func (f *binaryFixture) idle(item string) *v1.Agent {
	f.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		value, err := f.clients.Agents.GetAgent(context.Background(), connect.NewRequest(&v1.GetAgentRequest{ItemId: item}))
		if err == nil && value.Msg.Agent.Online && value.Msg.Agent.Status == v1.AgentStatus_AGENT_STATUS_IDLE {
			return value.Msg.Agent
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("agent did not become idle: %v %v", value, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
func (f *binaryFixture) historyReady(item string, count int) {
	f.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		value, err := f.clients.Agents.GetHistory(context.Background(), connect.NewRequest(&v1.GetHistoryRequest{ItemId: item}))
		if err == nil && len(value.Msg.Messages) >= count {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("history did not settle: %v %v", value, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestCLIAPIEndToEnd(t *testing.T) {
	f := newBinaryFixture(t)
	var added v1.AddRepositoryResponse
	f.json(&added, "repo", "add", "test", "--from", f.source, "--local")
	repo := added.Repository.Id
	var created v1.CreateItemResponse
	f.json(&created, "new", "--repo", repo, "--title", "API backlog")
	item := created.Item.Id
	var listed v1.ListItemsResponse
	f.json(&listed, "list")
	if len(listed.Items) != 1 || listed.Items[0].Id != item {
		t.Fatal(listed.String())
	}
	for _, args := range [][]string{{"info", "agent-socket"}, {"info", "config-file"}, {"config", "get", "plugins.setup.trust", "--origins"}, {"list", "--ids", "--no-agent"}, {"repo", "show", repo}} {
		if len(f.run(args...)) == 0 {
			t.Fatalf("missing human output: %v", args)
		}
	}
	f.run("attention", "unread", item)
	f.run("attention", "read", item)
	f.run("note", "--item", item, "Durable RPC note")
	var notes v1.ListNotesResponse
	f.json(&notes, "notes", item)
	if len(notes.Notes) != 1 {
		t.Fatal(notes.String())
	}
	f.run("state", "set", "active", item)
	var ensured v1.EnsureWorkspaceResponse
	f.json(&ensured, "workspace", "ensure", item)
	if ensured.Checkout.Claim == nil || ensured.Job.State != v1.JobState_JOB_STATE_SUCCEEDED {
		t.Fatal(ensured.String())
	}
	f.run("workspace", "setup", item)
	f.run("workspace", "status", item)
	f.run("workspace", "release", item)
	var started v1.CreateAndStartItemResponse
	f.json(&started, "new", "--repo", repo, "--title", "API fake agent", "--start", "--agent-mode", "headless", "--prompt", "first prompt")
	agentItem := started.Item.Id
	f.historyReady(agentItem, 2)
	first := f.idle(agentItem)
	if first.Generation == 0 || first.Mode != v1.AgentMode_AGENT_MODE_HEADLESS {
		t.Fatal(first.String())
	}
	f.run("agent", "control", "prompt", "--item", agentItem, "second prompt")
	f.historyReady(agentItem, 4)
	f.idle(agentItem)
	history, err := f.clients.Agents.GetHistory(context.Background(), connect.NewRequest(&v1.GetHistoryRequest{ItemId: agentItem}))
	if err != nil || len(history.Msg.Messages) < 4 {
		t.Fatalf("history: %v %v", history, err)
	}
	f.cliControlCompatibility(agentItem)
	f.run("agent", "settings", "set", "--item", agentItem, "--model", "test/model", "--reasoning", "low")
	f.idle(agentItem)
	settings, err := f.clients.Agents.GetAgentSettings(context.Background(), connect.NewRequest(&v1.GetAgentSettingsRequest{ItemId: agentItem}))
	if err != nil || settings.Msg.Overrides["reasoning"] != "low" {
		t.Fatalf("settings: %v %v", settings, err)
	}
	// A subscription gets a current snapshot without affecting runtime lifetime.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	stream, err := f.clients.Agents.WatchLiveContent(ctx, connect.NewRequest(&v1.WatchLiveContentRequest{ItemId: agentItem}))
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() || stream.Msg().GetSnapshot() == nil {
		t.Fatalf("live snapshot: %v", stream.Err())
	}
	cancel()
	stream.Close()
	f.idle(agentItem)
	var stopped v1.StopAgentResponse
	f.json(&stopped, "agent", "runtime", "stop", "--item", agentItem, "--force")
	f.run("resume", agentItem, "--agent-mode", "headless")
	second := f.idle(agentItem)
	if second.Generation <= first.Generation {
		t.Fatalf("generation did not advance: %d -> %d", first.Generation, second.Generation)
	}
	_, err = f.clients.Agents.RestartAgent(context.Background(), connect.NewRequest(&v1.RestartAgentRequest{ItemId: agentItem, ExpectedGeneration: first.Generation}))
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale generation: %v", err)
	}
	f.run("agent", "runtime", "stop", "--item", agentItem, "--force")
	f.run("close", agentItem, "--resolution", "completed")
	f.run("repo", "settings", "set", "--repo", repo, "plugins.setup.trust", "never")
	f.run("repo", "env", "set", "--repo", repo, "PORT=8080")
	// Direct curl proves the public socket speaks the declared Connect API.
	cmd := exec.Command("curl", "--fail", "--silent", "--show-error", "--unix-socket", f.socket, "-H", "Content-Type: application/json", "-H", "Connect-Protocol-Version: 1", "--data", "{}", "http://localhost/wi.core.v1.SystemService/GetInfo")
	cmd.Env = f.env
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"api_version":"1.0"`) {
		t.Fatalf("curl Connect: %v %s", err, out)
	}
	agent, err := apitransport.NewClients(f.agentSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	info, err := agent.System.GetInfo(context.Background(), connect.NewRequest(&v1.GetInfoRequest{}))
	if err != nil || info.Msg.Caller.GetAccess() != v1.CallerAccess_CALLER_ACCESS_AGENT {
		t.Fatalf("agent endpoint: %v %v", info, err)
	}
	_, err = agent.Items.SetItemState(context.Background(), connect.NewRequest(&v1.SetItemStateRequest{ItemId: item, State: v1.ItemState_ITEM_STATE_CLOSED, Resolution: v1.Resolution_RESOLUTION_COMPLETED}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("agent mutated lifecycle: %v", err)
	}
	f.tui(repo)
	f.cliTUICompatibility(repo)
	f.web(repo, item)
	f.restartJobs(repo, added.Job.Id)
}
func (f *binaryFixture) web(repo, item string) {
	f.t.Helper()
	log, err := os.Create(filepath.Join(f.root, "web.log"))
	if err != nil {
		f.t.Fatal(err)
	}
	defer log.Close()
	cmd := exec.Command(f.wi, "web")
	cmd.Env = f.env
	cmd.Dir = f.root
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Signal(os.Interrupt); _ = cmd.Wait() }()
	pattern := regexp.MustCompile(`Serving (http://127\.0\.0\.1:\d+)`)
	url := ""
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(log.Name())
		match := pattern.FindStringSubmatch(string(data))
		if len(match) > 1 {
			url = match[1]
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if url == "" {
		data, _ := os.ReadFile(log.Name())
		f.t.Fatalf("web did not start: %s", data)
	}
	curl := func(path string, body any) []byte {
		f.t.Helper()
		args := []string{"--fail", "--silent", "--show-error", "--max-time", "15"}
		if body != nil {
			data, _ := json.Marshal(body)
			args = append(args, "-H", "Origin: "+url, "-H", "Content-Type: application/json", "-H", "X-Wi-Client: smoke-tab", "--data", string(data))
		}
		args = append(args, url+path)
		command := exec.Command("curl", args...)
		command.Env = f.env
		out, err := command.CombinedOutput()
		if err != nil {
			f.t.Fatalf("curl %s: %v %s", path, err, out)
		}
		return out
	}
	for _, path := range []string{"/", "/items/" + item, "/settings/general", "/repositories/" + repo + "/settings/general"} {
		if !strings.Contains(strings.ToLower(string(curl(path, nil))), "<html") {
			f.t.Fatal("page missing HTML", path)
		}
	}
	for _, path := range []string{"/api/items", "/api/settings", "/api/repositories", "/api/agent-profiles", "/api/items/" + item + "/chat", "/api/items/" + item + "/settings", "/api/projects/" + repo + "/setup", "/api/projects/" + repo + "/env"} {
		if !json.Valid(curl(path, nil)) {
			f.t.Fatal("invalid backend JSON", path)
		}
	}
	curl("/api/items/"+item+"/notes", map[string]string{"text": "web RPC note"})
	curl("/api/backlog", map[string]string{"project_id": repo, "title": "Browser backlog", "description": "created through the adapter"})
	// Sending from a tab starts this active, currently released item in the daemon.
	curl("/api/items/"+item+"/messages", map[string]string{"text": "browser prompt", "delivery": "prompt", "client_id": "web-message-1"})
	f.historyReady(item, 2)
	f.idle(item)
	curl("/api/items/"+item+"/settings", map[string]any{"settings": map[string]string{"reasoning": "medium"}})
	f.idle(item)
	response, err := http.Get(url + "/api/items/" + item + "/chat")
	if err != nil {
		f.t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(string(data), "Echo: browser prompt") {
		f.t.Fatalf("browser conversation: %d %s", response.StatusCode, data)
	}
	curl("/api/items/"+item+"/close", map[string]any{"resolution": "completed", "force": true})
}
