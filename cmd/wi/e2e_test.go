package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
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
}
