// Package apitest starts isolated daemons for tests of the API and its
// clients. A fixture's daemon runs in the test process, composed as `wi daemon
// serve` composes one (internal/daemon), and serves its real socket.
package apitest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	driver "github.com/regb/workitem/agentdriver/protocol"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/agentdriver/pi"
	"github.com/regb/workitem/internal/apitransport"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/credential"
	"github.com/regb/workitem/internal/daemon"
	"github.com/regb/workitem/internal/daemonapi"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/plugins/bundled"
	"github.com/regb/workitem/internal/testutil"
	"github.com/regb/workitem/internal/xdg"
)

// Fixture is a running daemon and an operator's clients of it.
type Fixture struct {
	// Root is the fixture's private directory: its home, XDG directories
	// and sources live under it.
	Root string
	// Socket is the daemon's API socket.
	Socket string
	// Paths are the daemon's directories, as a wi process with Env resolves
	// them.
	Paths xdg.Paths
	// Env is the daemon's environment, which agent runtimes start from.
	Env []string
	// Clients are the operator's.
	Clients *apitransport.Clients
	// Binaries are set when the fixture runs agents.
	Binaries Binaries
	daemon   *daemon.Daemon
}

type options struct {
	agents bool
	config []string
}

// Option configures a fixture.
type Option func(*options)

// WithAgents runs agents: the daemon's wi executable is the test binary,
// standing in for wi (see runwi.Main), and its pi driver is Build's fake driver,
// so StartAgent starts a headless runtime host that runs the fake driver. Without it, agents cannot start, and the pi driver is an
// in-process stub that only creates and reads conversations.
func WithAgents() Option { return func(o *options) { o.agents = true } }

// WithConfig adds TOML to the daemon's configuration file, as an operator
// would write it.
func WithConfig(toml string) Option {
	return func(o *options) { o.config = append(o.config, toml) }
}

// New starts a daemon in a private root and waits until it answers. The
// daemon stops, and its agents with it, when the test ends.
func New(t testing.TB, opts ...Option) *Fixture {
	t.Helper()
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	root := testutil.ShortTempDir(t)
	f := &Fixture{Root: root, Env: testutil.IsolatedEnvironment(os.Environ(), root)}
	for _, part := range []string{"home", "data", "config/wi", "cache", "state", "run", "pi", "tmux"} {
		if err := os.MkdirAll(filepath.Join(root, part), 0700); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{}
	for _, entry := range f.Env {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	var err error
	if f.Paths, err = xdg.Resolve(xdg.Map(env), env["HOME"]); err != nil {
		t.Fatal(err)
	}
	if f.Socket, err = coordinator.SocketPath(f.Paths.RuntimeDir, f.Paths.DataRoot); err != nil {
		t.Fatal(err)
	}
	// Without agents the executable is absent: nothing the daemon would
	// start from it, a runtime host or a bundled driver, can run.
	self := filepath.Join(root, "absent-wi")
	config := strings.Join(o.config, "\n")
	if o.agents {
		// The test binary stands in for wi, so the runtime hosts it starts
		// are of the daemon's own build, which they require. The package's
		// TestMain must be runwi.Main.
		f.Binaries = Build(t)
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		self = executable
		f.Env = append(f.Env, RunAsWi+"=1")
		env[RunAsWi] = "1"
		config = fmt.Sprintf("[drivers.pi]\ncommand = %q\n", f.Binaries.Driver) + config
	}
	if err := os.WriteFile(f.Paths.ConfigFile, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, host, _, err := bundled.Load(f.Paths.ConfigFile, self)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.daemon, err = daemon.New(ctx, daemon.Options{Paths: f.Paths, Socket: f.Socket, Config: cfg, Host: host, Self: self, Env: env})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if !o.agents {
		f.daemon.App.ConversationBackends["pi"] = Backend{}
		if _, err := f.daemon.Core.LocalClient().ChangeAgentProfile(ctx, coordinator.ChangeAgentProfileCommand{Put: &model.AgentProfile{Name: "test", Driver: "pi"}}); err != nil {
			cancel()
			f.daemon.Close()
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- f.daemon.Serve(ctx) }()
	if f.Clients, err = apitransport.NewClients(f.Socket, credential.Source(credential.OperatorFile(f.Paths.DataRoot), credential.Operator)); err != nil {
		cancel()
		f.daemon.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if o.agents {
			if t.Failed() {
				f.logRuntimes(t)
			}
			f.stopAgents()
		}
		cancel()
		f.Clients.Close()
		f.daemon.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("API server did not stop")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := f.Clients.System.GetInfo(ctx, connect.NewRequest(&v1.GetInfoRequest{}))
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return f
}

// logRuntimes logs what the agent runtimes wrote, to explain a failure.
func (f *Fixture) logRuntimes(t testing.TB) {
	logs, _ := filepath.Glob(filepath.Join(f.Paths.DataStateRoot, "items", "*", "runtimes", "*", "runtime.log"))
	for _, path := range logs {
		data, _ := os.ReadFile(path)
		t.Logf("%s:\n%s", path, data)
	}
}

// stopAgents stops every agent, so no runtime host outlives the fixture.
func (f *Fixture) stopAgents() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	items, err := f.Clients.Items.ListItems(ctx, connect.NewRequest(&v1.ListItemsRequest{States: []v1.ItemState{v1.ItemState_ITEM_STATE_BACKLOG, v1.ItemState_ITEM_STATE_ACTIVE, v1.ItemState_ITEM_STATE_CLOSED}}))
	if err != nil {
		return
	}
	for _, item := range items.Msg.Items {
		_, _ = f.Clients.Agents.StopAgent(ctx, connect.NewRequest(&v1.StopAgentRequest{ItemId: item.Id, Force: true, ConfirmAttachedTerminal: true}))
	}
}

// The daemon's parts, for white-box tests. A test that uses one depends on
// the daemon's internals, not only on its API.

// Core is the daemon's coordinator.
func (f *Fixture) Core() *coordinator.Server { return f.daemon.Core }

// App is the daemon's application.
func (f *Fixture) App() *app.App { return f.daemon.App }

// API is the daemon's API server.
func (f *Fixture) API() *daemonapi.Server { return f.daemon.API }

// DataRoot is the daemon's data root.
func (f *Fixture) DataRoot() string { return f.Paths.DataRoot }

// Git runs git in dir and returns its output.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return string(out)
}

// Repository registers a new local repository with one empty commit on main,
// and returns its ID.
func (f *Fixture) Repository(t testing.TB, name string) string {
	t.Helper()
	source := filepath.Join(f.Root, "source-"+name)
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	Git(t, source, "init", "-b", "main")
	Git(t, source, "config", "user.name", "API Test")
	Git(t, source, "config", "user.email", "test@example.invalid")
	Git(t, source, "commit", "--allow-empty", "-m", "initial")
	response, err := f.Clients.Repositories.AddRepository(context.Background(), connect.NewRequest(&v1.AddRepositoryRequest{Name: name, Source: source, Branch: "main", Local: true}))
	if err != nil {
		t.Fatal(err)
	}
	job := Wait(t, f.Clients, response.Msg.Job)
	if job.RepositoryId == "" {
		t.Fatal("repository import did not retain its identity")
	}
	return job.RepositoryId
}

// Wait watches job until it ends, and fails the test unless it succeeded.
func Wait(t testing.TB, clients *apitransport.Clients, job *v1.Job) *v1.Job {
	t.Helper()
	if job == nil {
		t.Fatal("job absent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	stream, err := clients.Jobs.WatchJob(ctx, connect.NewRequest(&v1.WatchJobRequest{JobId: job.Id}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var latest *v1.Job
	for stream.Receive() {
		latest = stream.Msg().Job
	}
	if stream.Err() != nil {
		t.Fatal(stream.Err())
	}
	if latest == nil || latest.State != v1.JobState_JOB_STATE_SUCCEEDED {
		t.Fatalf("job failed: %v", latest)
	}
	return latest
}

// WaitIdle polls until item's agent is online and idle, and returns it.
func WaitIdle(t testing.TB, clients *apitransport.Clients, item string) *v1.Agent {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		value, err := clients.Agents.GetAgent(context.Background(), connect.NewRequest(&v1.GetAgentRequest{ItemId: item}))
		if err == nil && value.Msg.Agent.Online && value.Msg.Agent.Status == v1.AgentStatus_AGENT_STATUS_IDLE {
			return value.Msg.Agent
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent did not become idle: %v %v", value, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// WaitHistory polls until item's history has at least count messages, and
// returns it.
func WaitHistory(t testing.TB, clients *apitransport.Clients, item string, count int) []*v1.ConversationMessage {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		value, err := clients.Agents.GetHistory(context.Background(), connect.NewRequest(&v1.GetHistoryRequest{ItemId: item}))
		if err == nil && len(value.Msg.Messages) >= count {
			return value.Msg.Messages
		}
		if time.Now().After(deadline) {
			t.Fatalf("history did not settle: %v %v", value, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

type Backend struct{}

func (Backend) ValidateConfig(context.Context, string, json.RawMessage) error { return nil }
func (Backend) NewConversation(_ context.Context, id, _ string, _ json.RawMessage) (model.Conversation, error) {
	return pi.New("pi").NewConversation(id), nil
}
func (Backend) ConversationCWD(_ context.Context, path string) (string, error) {
	return pi.New("pi").ConversationCWD(path)
}
func (Backend) ForkConversation(ctx context.Context, source, target, cwd string) error {
	return pi.New("pi").ForkConversation(ctx, source, target, cwd)
}
func (Backend) ObserveConversation(_ context.Context, _ model.Conversation, path string, now time.Time) (driver.ConversationObservation, []string, error) {
	status, warnings := pi.ObserveConversationPath(path, now)
	return status, warnings, nil
}

// InvokeModel answers like a model asked for a title.
func (Backend) InvokeModel(context.Context, string, json.RawMessage, driver.ModelInvokeRequest) (string, error) {
	return "Suggested title", nil
}

// Clients connects with another credential: wi web's, or an item's.
func (f *Fixture) ClientsWith(t testing.TB, token func() (string, error)) *apitransport.Clients {
	t.Helper()
	clients, err := apitransport.NewClients(f.Socket, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(clients.Close)
	return clients
}

// WebClients connects as wi web does.
func (f *Fixture) WebClients(t testing.TB) *apitransport.Clients {
	return f.ClientsWith(t, credential.Source(credential.ProxyFile(f.DataRoot()), credential.Proxy))
}

// AgentClients connects as itemID's agent without starting one: it records
// a runtime owning the item and the credential that runtime was given, as
// starting an agent does. It returns the runtime, to stop or replace it.
func (f *Fixture) AgentClients(t testing.TB, itemID string) (*apitransport.Clients, model.AgentRuntime) {
	t.Helper()
	now := time.Now().UTC()
	runtime := model.AgentRuntime{ID: fmt.Sprintf("headless-%d", now.UnixNano()), WorkItemID: itemID, Mode: "headless", State: model.AgentRuntimeRunning, StartedAt: now, UpdatedAt: now}
	if err := f.App().Store.SaveAgentRuntime(context.Background(), itemID, runtime); err != nil {
		t.Fatal(err)
	}
	value, err := credential.New(credential.Item, itemID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Core().Database().SaveItemCredential(itemID, runtime.ID, value.Hash()); err != nil {
		t.Fatal(err)
	}
	return f.ClientsWith(t, func() (string, error) { return value.Token(), nil }), runtime
}
