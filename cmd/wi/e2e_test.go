package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/gen/wi/core/v1/corev1connect"
	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/apitransport"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/credential"
	"github.com/regb/workitem/internal/testutil"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type binaryFixture struct {
	root, wi, driver, source, socket string
	env                              []string
	clients                          *apitransport.Clients
	t                                *testing.T
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
	binaries := apitest.Build(t)
	f := &binaryFixture{root: root, wi: binaries.Wi, driver: binaries.Driver, source: filepath.Join(root, "source"), t: t}
	t.Cleanup(func() {
		if f.clients != nil {
			f.clients.Close()
		}
		cmd := exec.Command(f.wi, "daemon", "stop")
		cmd.Env = f.env
		cmd.Dir = root
		_ = cmd.Run()
		// The fixture's own tmux server, which TUI agents started, outlives
		// the daemon; left running, each run keeps its terminals until the
		// machine has none. Its socket is the fixture's alone.
		if f.env != nil {
			socket := filepath.Join(root, "tmux", fmt.Sprintf("tmux-%d", os.Getuid()), "default")
			tmux := exec.Command("tmux", "-S", socket, "kill-server")
			tmux.Env = f.env
			_ = tmux.Run()
		}
		time.Sleep(150 * time.Millisecond)
		_ = os.RemoveAll(root)
	})
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
	f.clients, err = apitransport.NewClients(f.socket, credential.Source(credential.OperatorFile(filepath.Join(root, "data", "wi")), credential.Operator))
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
	return apitest.WaitIdle(f.t, f.clients, item)
}
func (f *binaryFixture) historyReady(item string, count int) {
	f.t.Helper()
	apitest.WaitHistory(f.t, f.clients, item, count)
}

// TestCLIAPIEndToEnd proves what only the real binary can: daemon autostart,
// one CLI walk that starts an agent and sends to it, the agent credential
// files, the socket speaking Connect to curl, TUI agents in tmux, and a
// daemon restart keeping state. Behaviour reachable through the API is
// tested in internal/daemonapi.
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
	for _, args := range [][]string{{"info", "socket"}, {"info", "config-file"}, {"list", "--ids", "--no-agent"}, {"repo", "show", repo}} {
		if len(f.run(args...)) == 0 {
			t.Fatalf("missing human output: %v", args)
		}
	}
	var started v1.CreateAndStartItemResponse
	f.json(&started, "new", "--repo", repo, "--title", "API fake agent", "--start", "--agent-mode", "headless", "--prompt", "first prompt")
	agentItem := started.Item.Id
	f.historyReady(agentItem, 2)
	first := f.idle(agentItem)
	if start := started.Job.GetAgentStart(); started.Job.GetState() != v1.JobState_JOB_STATE_SUCCEEDED || start.GetGeneration() != first.Generation || start.GetMode() != v1.AgentMode_AGENT_MODE_HEADLESS {
		t.Fatal("start result", started.Job.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	send := exec.CommandContext(ctx, f.wi, "send", "--item", agentItem, "--stdin")
	send.Env = f.env
	send.Dir = f.root
	send.Stdin = strings.NewReader("second prompt")
	if out, err := send.CombinedOutput(); err != nil || !strings.Contains(string(out), "sent to") {
		t.Fatalf("send: %v %s", err, out)
	}
	f.historyReady(agentItem, 4)
	f.idle(agentItem)
	// Keep a copy of the agent's credential: its file goes with the runtime.
	running, _ := filepath.Glob(filepath.Join(f.root, "state", "wi", "*", "items", agentItem, "runtimes", "*", "credential"))
	if len(running) != 1 {
		t.Fatalf("the agent's credential: %v", running)
	}
	staleCopy := filepath.Join(f.root, "stale-credential")
	if data, err := os.ReadFile(running[0]); err != nil || os.WriteFile(staleCopy, data, 0o600) != nil {
		t.Fatalf("copy credential: %v", err)
	}
	f.run("agent", "runtime", "stop", "--item", agentItem, "--force")
	f.run("close", agentItem, "--resolution", "completed")
	// Direct curl proves the public socket speaks the declared Connect API,
	// to a caller presenting the operator's credential and to no other.
	operator, err := credential.Read(credential.OperatorFile(filepath.Join(f.root, "data", "wi")), credential.Operator)
	if err != nil {
		t.Fatal(err)
	}
	curl := func(headers ...string) ([]byte, error) {
		args := []string{"--silent", "--show-error", "--unix-socket", f.socket, "-H", "Content-Type: application/json", "-H", "Connect-Protocol-Version: 1", "--data", "{}"}
		for _, header := range headers {
			args = append(args, "-H", header)
		}
		cmd := exec.Command("curl", append(args, "http://localhost/wi.core.v1.SystemService/GetInfo")...)
		cmd.Env = f.env
		return cmd.CombinedOutput()
	}
	if out, err := curl("Authorization: Bearer " + operator.Token()); err != nil || !strings.Contains(string(out), `"api_version":"1.0"`) || !strings.Contains(string(out), `"principal":"user:operator"`) {
		t.Fatalf("curl Connect: %v %s", err, out)
	}
	if out, err := curl(); err != nil || !strings.Contains(string(out), `"unauthenticated"`) {
		t.Fatalf("curl without a credential: %v %s", err, out)
	}
	// An agent whose runtime has stopped is refused, and does not fall back
	// to acting as the operator.
	if gone, _ := filepath.Glob(filepath.Join(f.root, "state", "wi", "*", "items", agentItem, "runtimes", "*", "credential")); len(gone) != 0 {
		t.Fatalf("a stopped runtime's credential file remains: %v", gone)
	}
	cmd := exec.Command(f.wi, "list", "--json")
	cmd.Env = append(append([]string{}, f.env...), "WI_ITEM_CREDENTIAL_FILE="+staleCopy)
	cmd.Dir = f.root
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "no longer valid") {
		t.Fatalf("a stopped agent's credential: %v %s", err, out)
	}
	f.tui(repo)
	// A restarted daemon keeps its items and its finished jobs.
	f.run("daemon", "stop")
	f.run("daemon", "start")
	job, err := f.clients.Jobs.GetJob(context.Background(), connect.NewRequest(&v1.GetJobRequest{JobId: added.Job.Id}))
	if err != nil || job.Msg.Job.State != v1.JobState_JOB_STATE_SUCCEEDED {
		t.Fatalf("finished job after restart: %v %v", job, err)
	}
	f.json(&listed, "list")
	if len(listed.Items) != 1 || listed.Items[0].Id != item {
		t.Fatalf("items after restart: %s", listed.String())
	}
	f.web(repo, item)
}

// webHeaders sends what a tab sends with each call.
type webHeaders struct{ origin string }

func (h webHeaders) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Origin", h.origin)
	r.Header.Set("X-Wi-Client", "smoke-tab")
	return http.DefaultTransport.RoundTrip(r)
}

// webWatch follows the stream a tab opens for an item through wi web: it
// starts with where the change feed is, the unfinished jobs and the item's
// live content.
func (f *binaryFixture) webWatch(url, item string) {
	f.t.Helper()
	events := corev1connect.NewEventServiceClient(&http.Client{Transport: webHeaders{url}}, url)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := events.Watch(ctx, connect.NewRequest(&v1.WatchRequest{ItemId: item}))
	if err != nil {
		f.t.Fatal(err)
	}
	defer stream.Close()
	seen := map[string]bool{}
	for len(seen) < 3 && stream.Receive() {
		switch stream.Msg().Event.(type) {
		case *v1.WatchResponse_Changes:
			seen["changes"] = true
		case *v1.WatchResponse_Jobs:
			seen["jobs"] = stream.Msg().GetJobs().Snapshot || seen["jobs"]
		case *v1.WatchResponse_Content:
			seen["content"] = stream.Msg().GetContent().GetSnapshot() != nil || seen["content"]
		}
	}
	if !seen["changes"] || !seen["jobs"] || !seen["content"] {
		f.t.Fatalf("watch through wi web saw %v: %v", seen, stream.Err())
	}
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
	if settings := curl("/wi.core.v1.AgentService/GetAgentSettings", map[string]string{"item_id": item}); !strings.Contains(string(settings), `"agent_profile"`) {
		f.t.Fatalf("item settings through wi web: %s", settings)
	}
	// The page calls the daemon's API through wi web, as a Connect client:
	// the dashboard, the conversation, and one stream for what changes.
	if list := curl("/wi.core.v1.ItemService/ListItems", map[string]any{"order": "LIST_ORDER_ATTENTION", "states": []string{"ITEM_STATE_ACTIVE", "ITEM_STATE_BACKLOG", "ITEM_STATE_CLOSED"}}); !strings.Contains(string(list), item) || !strings.Contains(string(list), `"section":"LIST_SECTION_`) {
		f.t.Fatalf("dashboard through wi web: %s", list)
	}
	if view := curl("/wi.core.v1.ViewService/GetItemView", map[string]string{"item_id": item}); !strings.Contains(string(view), item) {
		f.t.Fatalf("conversation through wi web: %s", view)
	}
	f.webWatch(url, item)
	if profiles := curl("/wi.core.v1.AgentProfileService/ListAgentProfiles", map[string]string{}); !strings.Contains(string(profiles), `"default_profile":"pi"`) {
		f.t.Fatalf("API through wi web: %s", profiles)
	}
	if repos := curl("/wi.core.v1.RepositoryService/ListRepositories", map[string]string{}); !strings.Contains(string(repos), repo) {
		f.t.Fatalf("repositories through wi web: %s", repos)
	}
	if setup := curl("/wi.core.v1.RepositoryService/GetCheckoutSetup", map[string]string{"repositoryId": repo}); !strings.Contains(string(setup), `"setup"`) {
		f.t.Fatalf("checkout setup through wi web: %s", setup)
	}
	curl("/wi.core.v1.ItemService/AddNote", map[string]string{"itemId": item, "text": "web RPC note"})
	curl("/wi.core.v1.ItemService/CreateItem", map[string]string{"repository_id": repo, "title": "Browser backlog", "description": "created through wi web", "request_id": "web-backlog-1"})
	// Sending from a tab starts this active, currently released item in the
	// daemon: the call answers with the job starting it.
	if sent := curl("/wi.core.v1.AgentService/SendMessage", map[string]string{"item_id": item, "text": "browser prompt", "delivery": "DELIVERY_PROMPT", "client_message_id": "web-message-1", "start_mode": "AGENT_MODE_HEADLESS"}); !strings.Contains(string(sent), `"job"`) {
		f.t.Fatalf("send to a released item: %s", sent)
	}
	f.historyReady(item, 2)
	f.idle(item)
	curl("/wi.core.v1.AgentService/UpdateAgentSettings", map[string]any{"item_id": item, "settings": map[string]string{"reasoning": "medium"}})
	f.idle(item)
	if view := curl("/wi.core.v1.ViewService/GetItemView", map[string]string{"item_id": item}); !strings.Contains(string(view), "Echo: browser prompt") {
		f.t.Fatalf("browser conversation: %s", view)
	}
	f.reviewWeb(item, url, curl)
	curl("/wi.core.v1.LifecycleService/CloseItem", map[string]any{"item_id": item, "resolution": "RESOLUTION_COMPLETED", "force": true})
}
