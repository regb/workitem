package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/gen/wi/core/v1/corev1connect"
	"google.golang.org/protobuf/proto"
)

type captureAgents struct {
	corev1connect.AgentServiceClient
	send  func(context.Context, *connect.Request[v1.SendMessageRequest]) (*connect.Response[v1.SendMessageResponse], error)
	abort func(context.Context, *connect.Request[v1.AbortTurnRequest]) (*connect.Response[v1.AbortTurnResponse], error)
	get   func(context.Context, *connect.Request[v1.GetAgentRequest]) (*connect.Response[v1.GetAgentResponse], error)
}

func (c captureAgents) SendMessage(ctx context.Context, r *connect.Request[v1.SendMessageRequest]) (*connect.Response[v1.SendMessageResponse], error) {
	return c.send(ctx, r)
}
func (c captureAgents) AbortTurn(ctx context.Context, r *connect.Request[v1.AbortTurnRequest]) (*connect.Response[v1.AbortTurnResponse], error) {
	return c.abort(ctx, r)
}
func (c captureAgents) GetAgent(ctx context.Context, r *connect.Request[v1.GetAgentRequest]) (*connect.Response[v1.GetAgentResponse], error) {
	return c.get(ctx, r)
}

type captureControl struct {
	corev1connect.ControlServiceClient
}

func (c captureControl) GetControl(context.Context, *connect.Request[v1.GetControlRequest]) (*connect.Response[v1.GetControlResponse], error) {
	return connect.NewResponse(&v1.GetControlResponse{Control: &v1.ControlStatus{Plan: &v1.ControlPlan{PlanToken: "plan"}}}), nil
}
func (c captureControl) AcquireControl(_ context.Context, r *connect.Request[v1.AcquireControlRequest]) (*connect.Response[v1.AcquireControlResponse], error) {
	return connect.NewResponse(&v1.AcquireControlResponse{Control: &v1.ControlStatus{Lease: &v1.ControlLease{Id: "lease", ClientId: r.Msg.ClientId}}}), nil
}

type captureItems struct {
	corev1connect.ItemServiceClient
	list func(context.Context, *connect.Request[v1.ListItemsRequest]) (*connect.Response[v1.ListItemsResponse], error)
}

func (c captureItems) ListItems(ctx context.Context, r *connect.Request[v1.ListItemsRequest]) (*connect.Response[v1.ListItemsResponse], error) {
	return c.list(ctx, r)
}

type captureLifecycle struct {
	corev1connect.LifecycleServiceClient
	start func(context.Context, *connect.Request[v1.StartItemRequest]) (*connect.Response[v1.StartItemResponse], error)
}

func (c captureLifecycle) StartItem(ctx context.Context, r *connect.Request[v1.StartItemRequest]) (*connect.Response[v1.StartItemResponse], error) {
	return c.start(ctx, r)
}

func TestRPCTextRenderingIsNotProtoJSON(t *testing.T) {
	for _, message := range []proto.Message{
		&v1.GetAgentResponse{Agent: &v1.Agent{AgentProfile: "pi", Online: true, Status: v1.AgentStatus_AGENT_STATUS_IDLE, Mode: v1.AgentMode_AGENT_MODE_TUI, Generation: 2}},
		&v1.GetAgentSettingsResponse{Driver: "pi", Values: map[string]string{"model": "test/model"}},
		&v1.GetRepositoryResponse{Repository: &v1.Repository{Name: "project", Id: "repo", IntegrationBranch: "main"}},
		&v1.GetWorkspaceResponse{Status: v1.WorktreeStatus_WORKTREE_STATUS_CLEAN},
		&v1.GetControlResponse{Control: &v1.ControlStatus{Yours: true}},
		&v1.ListNotesResponse{Notes: []*v1.Note{{Text: "human note"}}},
		&v1.StartItemResponse{Item: &v1.Item{Id: "item", Title: "task", State: v1.ItemState_ITEM_STATE_ACTIVE}},
		&v1.GetHealthResponse{Healthy: false},
	} {
		t.Run(string(message.ProtoReflect().Descriptor().Name()), func(t *testing.T) {
			var out bytes.Buffer
			cfg := Config{Stdout: &out}
			if err := rpcEmit(cfg, false, message); err != nil {
				t.Fatal(err)
			}
			if out.Len() == 0 || json.Valid(out.Bytes()) || strings.Contains(out.String(), "ITEM_STATE_") {
				t.Fatalf("not human text: %s", out.String())
			}
			out.Reset()
			if err := rpcEmit(cfg, true, message); err != nil || !json.Valid(out.Bytes()) {
				t.Fatalf("not protojson: %s %v", out.String(), err)
			}
		})
	}
}
func TestControlSendFlagsAndAbortActor(t *testing.T) {
	f := newClientFixture(t)
	var created v1.CreateItemResponse
	f.decode(t, f.run(t, "new", "--repo", f.repo, "--title", "control flags", "--json"), &created)
	f.cfg.Env["WI_ID"] = created.Item.Id
	file := filepath.Join(f.cfg.CWD, "prompt.txt")
	if err := os.WriteFile(file, []byte("file message"), 0600); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(f.cfg.CWD, "image.png")
	imageData := []byte("\x89PNG\r\n\x1a\nimage")
	if err := os.WriteFile(image, imageData, 0600); err != nil {
		t.Fatal(err)
	}
	var sent *v1.SendMessageRequest
	actor := ""
	aborted := false
	f.cfg.API.Agents = captureAgents{send: func(_ context.Context, r *connect.Request[v1.SendMessageRequest]) (*connect.Response[v1.SendMessageResponse], error) {
		sent = r.Msg
		actor = r.Header().Get("Wi-Control-Actor")
		return connect.NewResponse(&v1.SendMessageResponse{Generation: 7}), nil
	}, abort: func(_ context.Context, r *connect.Request[v1.AbortTurnRequest]) (*connect.Response[v1.AbortTurnResponse], error) {
		aborted = true
		actor = r.Header().Get("Wi-Control-Actor")
		return connect.NewResponse(&v1.AbortTurnResponse{}), nil
	}}
	f.cfg.API.Control = captureControl{}
	for _, test := range []struct {
		name          string
		args          []string
		stdin, text   string
		follow, image bool
	}{
		{name: "text", args: []string{"--actor", "automation", "hello", "world"}, text: "hello world"},
		{name: "literal", args: []string{"--", "--not-a-flag"}, text: "--not-a-flag"},
		{name: "file", args: []string{"--file", file}, text: "file message"},
		{name: "stdin", args: []string{"--stdin"}, stdin: "stdin message\n", text: "stdin message"},
		{name: "follow", args: []string{"--follow-up", "later"}, text: "later", follow: true},
		{name: "image", args: []string{"--image", image}, image: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			sent = nil
			f.cfg.Stdin = strings.NewReader(test.stdin)
			f.run(t, append([]string{"agent", "control", "send", "--json"}, test.args...)...)
			if sent == nil || sent.Text != test.text || (sent.Delivery == v1.Delivery_DELIVERY_FOLLOW_UP) != test.follow {
				t.Fatalf("request: %v", sent)
			}
			if test.image && (len(sent.Images) != 1 || !bytes.Equal(sent.Images[0].Data, imageData)) {
				t.Fatal(sent)
			}
			if test.name == "text" && actor != "automation" {
				t.Fatal(actor)
			}
		})
	}
	f.run(t, "agent", "control", "abort", "--actor", "automation", "--json")
	if !aborted || actor != "automation" {
		t.Fatalf("abort: %t %q", aborted, actor)
	}
	for _, args := range [][]string{{"send", "--stdin", "text"}, {"abort", "text"}, {"abort", "--image", image}, {"send", "--file", file, "text"}, {"monitor", "--limit", "-1"}} {
		command := append([]string{"agent", "control"}, args...)
		if args[0] == "monitor" {
			command = append([]string{"agent"}, args...)
		}
		if code := Run(context.Background(), command, f.cfg); code != ExitUsage {
			t.Fatalf("%v returned %d", command, code)
		}
	}
}
func TestListFlagsAndReadableGroups(t *testing.T) {
	f := newClientFixture(t)
	var open, closed v1.CreateItemResponse
	f.decode(t, f.run(t, "new", "--repo", f.repo, "--title", "open task", "--label", "team", "--json"), &open)
	f.decode(t, f.run(t, "new", "--repo", f.repo, "--title", "closed task", "--json"), &closed)
	f.run(t, "state", "set", "closed", closed.Item.Id, "--resolution", "completed", "--json")
	for _, test := range []struct {
		args  []string
		count int
	}{{[]string{"--closed"}, 1}, {[]string{"--all"}, 2}, {[]string{"--state", "all"}, 2}, {[]string{"--state", "backlog"}, 1}, {[]string{"--label", "team"}, 1}, {[]string{"--no-agent"}, 1}} {
		var listed v1.ListItemsResponse
		f.decode(t, f.run(t, append([]string{"list", "--json"}, test.args...)...), &listed)
		if len(listed.Items) != test.count {
			t.Fatalf("%v: %v", test.args, listed.String())
		}
	}
	text := string(f.run(t, "list", "--ids", "--no-agent"))
	if !strings.Contains(text, "BACKLOG") || !strings.Contains(text, "ID") || !strings.Contains(text, "open-task") || strings.Contains(text, "AGENT") {
		t.Fatal(text)
	}
	for _, args := range [][]string{{"list", "--all", "--closed"}, {"list", "--closed", "--state", "active"}, {"list", "unexpected"}} {
		if code := Run(context.Background(), args, f.cfg); code != ExitUsage {
			t.Fatalf("%v: %d", args, code)
		}
	}
}
func TestAttentionConfigAndInfoRestored(t *testing.T) {
	f := newClientFixture(t)
	var created v1.CreateItemResponse
	f.decode(t, f.run(t, "new", "--repo", f.repo, "--title", "read state", "--json"), &created)
	f.cfg.Env["WI_ID"] = created.Item.Id
	f.run(t, "attention", "unread")
	var shown v1.GetItemResponse
	f.decode(t, f.run(t, "show", "--json"), &shown)
	if !shown.Observation.Unread {
		t.Fatal(shown.String())
	}
	f.run(t, "attention", "read")
	f.decode(t, f.run(t, "show", "--json"), &shown)
	if shown.Observation.Unread {
		t.Fatal(shown.String())
	}
	for _, args := range [][]string{{"config", "get", "deep_work.max_active"}, {"config", "get", "item.defaults.labels"}, {"config", "get", "plugins.setup.trust", "--origins"}, {"config", "plugins"}, {"info", "agent-socket"}, {"info", "config-file"}} {
		out := f.run(t, args...)
		if len(out) == 0 || strings.HasPrefix(strings.TrimSpace(out), "{") {
			t.Fatalf("%v: %s", args, out)
		}
	}
}
func TestLifecycleForceReachesStartOptions(t *testing.T) {
	for _, command := range []string{"start", "resume", "reopen"} {
		t.Run(command, func(t *testing.T) {
			f := newClientFixture(t)
			var created v1.CreateItemResponse
			f.decode(t, f.run(t, "new", "--repo", f.repo, "--title", "force", "--json"), &created)
			if command == "reopen" {
				f.run(t, "state", "set", "closed", created.Item.Id, "--resolution", "completed", "--json")
			}
			captured := false
			f.cfg.API.Lifecycle = captureLifecycle{LifecycleServiceClient: f.cfg.API.Lifecycle, start: func(_ context.Context, r *connect.Request[v1.StartItemRequest]) (*connect.Response[v1.StartItemResponse], error) {
				captured = r.Msg.Start.OverrideCapacity
				return connect.NewResponse(&v1.StartItemResponse{Item: created.Item}), nil
			}}
			f.run(t, command, created.Item.Id, "--force", "--json")
			if !captured {
				t.Fatal("override_capacity was dropped")
			}
		})
	}
}
func TestTerminalTargetUsesOriginatingClient(t *testing.T) {
	for _, inTmux := range []bool{false, true} {
		cfg := Config{Env: map[string]string{}}
		var args []string
		cfg.RunTerminal = func(_ context.Context, argv []string) error { args = argv; return nil }
		if inTmux {
			cfg.Env["WI_TMUX_CLIENT"] = "client-7"
		}
		if err := runTerminalTarget(context.Background(), cfg, &v1.Terminal{TmuxSocket: "/tmp/owned.sock", Session: "wi-owned", Window: "agent"}); err != nil {
			t.Fatal(err)
		}
		text := strings.Join(args, " ")
		if !strings.Contains(text, "-S /tmp/owned.sock") || !strings.Contains(text, "wi-owned:agent") {
			t.Fatal(args)
		}
		if inTmux && !strings.Contains(text, "switch-client -c client-7") {
			t.Fatal(args)
		}
		if !inTmux && !strings.Contains(text, "attach-session") {
			t.Fatal(args)
		}
	}
}
func TestNextUsesConfigLabelsAndMarksItemLeftRead(t *testing.T) {
	f := newClientFixture(t)
	var left, target v1.CreateItemResponse
	f.decode(t, f.run(t, "new", "--repo", f.repo, "--title", "left", "--json"), &left)
	f.decode(t, f.run(t, "new", "--repo", f.repo, "--title", "target", "--json"), &target)
	f.cfg.Env["WI_ID"] = left.Item.Id
	f.cfg.GlobalConfig.List.Labels = []string{"+team"}
	actual := f.cfg.API.Items
	f.cfg.API.Items = captureItems{ItemServiceClient: actual, list: func(_ context.Context, r *connect.Request[v1.ListItemsRequest]) (*connect.Response[v1.ListItemsResponse], error) {
		if len(r.Msg.LabelRules) != 1 || r.Msg.LabelRules[0].GetRequire() != "team" {
			t.Fatalf("lost defaults: %v", r.Msg)
		}
		return connect.NewResponse(&v1.ListItemsResponse{Items: []*v1.ItemSummary{{Id: target.Item.Id, Observation: &v1.ItemObservation{NeedsAttention: true}}}}), nil
	}}
	f.cfg.API.Control = captureControl{}
	f.cfg.API.Agents = captureAgents{get: func(_ context.Context, r *connect.Request[v1.GetAgentRequest]) (*connect.Response[v1.GetAgentResponse], error) {
		return connect.NewResponse(&v1.GetAgentResponse{Agent: &v1.Agent{Online: true, Mode: v1.AgentMode_AGENT_MODE_TUI, Terminal: &v1.Terminal{Session: "owned", Window: "agent"}}}), nil
	}}
	attached := 0
	f.cfg.Interactive = func() bool { return true }
	f.cfg.RunTerminal = func(context.Context, []string) error { attached++; return nil }
	f.run(t, "next", "--json")
	if attached != 0 {
		t.Fatal("--json attached a terminal")
	}
	f.run(t, "next")
	if attached != 1 {
		t.Fatal("interactive next did not enter the TUI")
	}
	shown, err := actual.GetItem(context.Background(), connect.NewRequest(&v1.GetItemRequest{ItemId: left.Item.Id}))
	if err != nil || shown.Msg.Observation.GetActivity().GetLastReadAt() == nil {
		t.Fatalf("left item not marked: %v %v", shown, err)
	}
}
