// Package apitest supplies isolated real-socket API fixtures for client tests.
package apitest

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	driver "github.com/regb/workitem/agentdriver/protocol"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/agentdriver/pi"
	"github.com/regb/workitem/internal/apitransport"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/daemonapi"
	"github.com/regb/workitem/internal/daemonapp"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/plugins/bundled"
	"github.com/regb/workitem/internal/store"
)

type Fixture struct {
	Root, Socket, AgentSocket string
	Core                      *coordinator.Server
	App                       *app.App
	Server                    *daemonapi.Server
	Clients                   *apitransport.Clients
	Config                    config.Config
}

func New(t testing.TB) *Fixture {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "wi-api-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	cfg := config.Default()
	self, _ := os.Executable()
	_, host, _, err := bundled.Load(filepath.Join(root, "absent.toml"), self)
	if err != nil {
		t.Fatal(err)
	}
	socket, agentSocket := filepath.Join(root, "api.sock"), filepath.Join(root, "agent.sock")
	core, err := coordinator.NewServerWithAgentSocket(filepath.Join(root, "data"), socket, agentSocket)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(filepath.Join(root, "data"))
	application := app.New(st, gitpkg.New("git"))
	daemonapp.Bind(application, st, core.LocalClient())
	application.LoadAgentProfiles = core.LocalClient().AgentProfiles
	application.SelfPath = filepath.Join(root, "unavailable-wi")
	application.AgentRuntimeStateRoot = filepath.Join(root, "state")
	application.AgentRuntimeSocketRoot = filepath.Join(root, "run")
	application.Plugins = host
	application.ConversationBackends["pi"] = Backend{}
	host.ReadRepository = func(ctx context.Context, id string) (model.RepositorySettings, error) {
		v, e := core.LocalClient().RepositorySettings(ctx, id)
		return v.Settings, e
	}
	application.Preparation = host.Preparation(nil)
	_, err = core.LocalClient().ChangeAgentProfile(context.Background(), coordinator.ChangeAgentProfileCommand{Put: &model.AgentProfile{Name: "test", Driver: "pi"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	server, err := daemonapi.New(ctx, core, application, cfg, host, filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	core.SetBeforeClose(server.Close)
	core.ConfigureRepositorySettings(host.Registry.Options())
	done := make(chan error, 1)
	go func() { done <- core.ServeAPI(ctx, server.Handler()) }()
	clients, err := apitransport.NewClients(socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		clients.Close()
		core.Close()
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
		_, err := clients.System.GetInfo(ctx, connect.NewRequest(&v1.GetInfoRequest{}))
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return &Fixture{Root: root, Socket: socket, AgentSocket: agentSocket, Core: core, App: application, Server: server, Clients: clients, Config: cfg}
}
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return string(out)
}
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
	return job.RepositoryId
}
func Wait(t testing.TB, clients *apitransport.Clients, job *v1.Job) *v1.Job {
	t.Helper()
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
func (f *Fixture) Item(t testing.TB, repo, title string) *v1.Item {
	t.Helper()
	value, err := f.Clients.Items.CreateItem(context.Background(), connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: title}))
	if err != nil {
		t.Fatal(err)
	}
	return value.Msg.Item
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
func (Backend) GenerateTitle(context.Context, string, string, json.RawMessage) (string, error) {
	return "Suggested title", nil
}
