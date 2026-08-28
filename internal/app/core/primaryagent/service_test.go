package primaryagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/runtimepath"
	"github.com/regb/workitem/internal/testutil"
)

type runtimeStore struct {
	itemDir       string
	runtime       *model.AgentRuntime
	savedManifest *model.Manifest
}

func (s *runtimeStore) LoadAgentRuntime(string) (*model.AgentRuntime, error)       { return s.runtime, nil }
func (s *runtimeStore) ItemDir(string) string                                      { return s.itemDir }
func (s *runtimeStore) LoadTerminalRuntime(string) (*model.TerminalRuntime, error) { return nil, nil }
func (s *runtimeStore) SaveTerminalRuntime(context.Context, string, model.TerminalRuntime) error {
	return nil
}
func (s *runtimeStore) SaveAgentRuntime(context.Context, string, model.AgentRuntime) error {
	return nil
}
func (s *runtimeStore) SaveManifest(_ context.Context, m model.Manifest) error {
	s.savedManifest = &m
	return nil
}
func (s *runtimeStore) AppendEvent(context.Context, string, model.Event) error { return nil }

type process bool

func (p process) Alive(int) bool { return bool(p) }
func (p process) Info(pid int) (model.ProcessInfo, error) {
	if !p.Alive(pid) {
		return model.ProcessInfo{}, os.ErrNotExist
	}
	return model.ProcessInfo{PID: pid, PGRP: pid, StartTime: uint64(pid), State: "S"}, nil
}
func (p process) TerminateGroup(int) error { return nil }
func (p process) FindDescendant(int, []string) (model.ProcessInfo, bool, error) {
	return model.ProcessInfo{}, false, nil
}

type identityProcess struct {
	info       model.ProcessInfo
	terminated bool
}

func (p *identityProcess) Alive(int) bool { return p.info.State != "Z" }
func (p *identityProcess) Info(int) (model.ProcessInfo, error) {
	return p.info, nil
}
func (p *identityProcess) TerminateGroup(int) error {
	p.terminated = true
	return nil
}
func (p *identityProcess) FindDescendant(int, []string) (model.ProcessInfo, bool, error) {
	return model.ProcessInfo{}, false, nil
}

type observationTmux struct {
	exists    bool
	paneCalls int
}

func (t *observationTmux) HasSession(context.Context, string) (bool, error) {
	return t.exists, nil
}
func (t *observationTmux) PaneInfo(context.Context, string) (model.TerminalPaneInfo, error) {
	t.paneCalls++
	return model.TerminalPaneInfo{PanePID: 123}, nil
}

func TestInspectPrimaryAgentProcessTreatsAbsentTmuxServerAsNormalOfflineState(t *testing.T) {
	tmux := &observationTmux{}
	s := New(&runtimeStore{}, process(true), nil, tmux, nil, nil, time.Now, nil)
	status := s.inspectPrimaryAgentProcess(context.Background(), model.Manifest{}, &model.TerminalRuntime{TmuxWindow: "agent", TmuxPanePID: 123}, nil)
	if status.UnavailableReason != "" {
		t.Fatalf("unavailable reason = %q", status.UnavailableReason)
	}
	if status.Online || status.TmuxPaneAlive {
		t.Fatalf("absent tmux session reused stale pane state: %+v", status)
	}
	if tmux.paneCalls != 0 {
		t.Fatalf("PaneInfo calls = %d, want 0", tmux.paneCalls)
	}
}

func TestRuntimeStatusWarnsForInvalidMode(t *testing.T) {
	st := &runtimeStore{runtime: &model.AgentRuntime{ID: "runtime-1", WorkItemID: "item-1", Mode: "print", HostPID: 42, HostProcessGroup: 42, HostStartTime: 42}}
	resolve := func(context.Context, contract.ResolveOptions) (model.Manifest, error) {
		return model.Manifest{ID: "item-1"}, nil
	}
	status, err := New(st, process(true), nil, nil, resolve, nil, nil, nil).RuntimeStatus(context.Background(), contract.ResolveOptions{})
	if err != nil || !status.Online || len(status.Warnings) != 1 || !strings.Contains(status.Warnings[0], "invalid agent runtime mode") {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if status.Capabilities != (agent.Capabilities{}) {
		t.Fatalf("capabilities=%+v", status.Capabilities)
	}
}

func TestSubmitControlPopulatesProtocolIdentity(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "wi-control-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	st := &runtimeStore{itemDir: root}
	s := New(st, process(true), nil, nil, nil, nil, time.Now, nil)
	s.RuntimeSocketRoot = filepath.Join(root, "runtime")
	runtime := model.AgentRuntime{ID: "runtime-1", WorkItemID: "item-1"}
	server, err := agent.ListenControlSocket(filepath.Join(s.RuntimeSocketRoot, filepath.FromSlash(runtimepath.ControlSocket("item-1", runtime.ID))))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	seen := make(chan agent.ControlCommand, 1)
	go func() {
		request := <-server.Requests()
		seen <- request.Command
		request.Respond(nil)
	}()
	if err := s.SubmitControl(context.Background(), "item-1", runtime, agent.ControlCommand{ID: "command-1", Type: agent.CommandShutdown}); err != nil {
		t.Fatal(err)
	}
	command := <-seen
	if command.ProtocolVersion != agent.RuntimeProtocolVersion || command.RuntimeID != runtime.ID || command.WorkItemID != "item-1" {
		t.Fatalf("command = %+v", command)
	}
}

func TestObserveOwnershipRejectsReusedPID(t *testing.T) {
	s := New(&runtimeStore{}, process(true), nil, nil, nil, nil, nil, nil)
	ownership := s.ObserveOwnership(&model.AgentRuntime{ID: "runtime", HostPID: 42, HostProcessGroup: 42, HostStartTime: 999})
	if ownership.ProcessAlive || ownership.IdentityVerified || ownership.ControlAvailable {
		t.Fatalf("reused PID accepted as runtime owner: %+v", ownership)
	}
}

func TestObserveOwnershipRequiresLiveControlSocket(t *testing.T) {
	root := testutil.ShortTempDir(t)
	s := New(&runtimeStore{}, process(true), nil, nil, nil, nil, nil, nil)
	s.RuntimeSocketRoot = root
	withoutSocket := s.ObserveOwnership(&model.AgentRuntime{ID: "without-socket", WorkItemID: "item-1", Mode: string(agent.ModeTUI), HostPID: 42, HostProcessGroup: 42, HostStartTime: 42})
	if !withoutSocket.ProcessAlive || withoutSocket.ControlAvailable || withoutSocket.Mode != string(agent.ModeTUI) {
		t.Fatalf("ownership without socket=%+v", withoutSocket)
	}
	controlledRuntime := model.AgentRuntime{ID: "controlled", WorkItemID: "item-1", Mode: string(agent.ModeRPC), HostPID: 42, HostProcessGroup: 42, HostStartTime: 42}
	server, err := agent.ListenControlSocket(filepath.Join(root, filepath.FromSlash(runtimepath.ControlSocket("item-1", controlledRuntime.ID))))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	controlled := s.ObserveOwnership(&controlledRuntime)
	if !controlled.ProcessAlive || !controlled.IdentityVerified || !controlled.ControlAvailable {
		t.Fatalf("controlled ownership=%+v", controlled)
	}
	offline := New(&runtimeStore{}, process(false), nil, nil, nil, nil, nil, nil).ObserveOwnership(&model.AgentRuntime{ID: "offline", WorkItemID: "item-1", HostPID: 42})
	if offline.ProcessAlive || offline.ControlAvailable {
		t.Fatalf("offline ownership=%+v", offline)
	}
}

func TestRuntimeStatus(t *testing.T) {
	st := &runtimeStore{runtime: &model.AgentRuntime{ID: "runtime-1", WorkItemID: "item-1", Mode: string(agent.ModeRPC), HostPID: 42, HostProcessGroup: 42, HostStartTime: 42}}
	resolve := func(context.Context, contract.ResolveOptions) (model.Manifest, error) {
		return model.Manifest{ID: "item-1"}, nil
	}
	s := New(st, process(true), nil, nil, resolve, nil, nil, nil)
	status, err := s.RuntimeStatus(context.Background(), contract.ResolveOptions{})
	if err != nil || !status.Online || !status.Ownership.ProcessAlive || status.Ownership.ControlAvailable || status.Runtime.ID != "runtime-1" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestStopForceFallsBackToTerminateGroupWhenSocketUnavailable(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "wi-stop-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	now := time.Now().UTC()
	runtime := &model.AgentRuntime{ID: "runtime-1", WorkItemID: "item-1", Mode: string(agent.ModeTUI), HostPID: 4242, HostProcessGroup: 4242, HostStartTime: 4242}
	st := &runtimeStore{itemDir: root, runtime: runtime}
	p := &identityProcess{info: model.ProcessInfo{PID: 4242, PGRP: 4242, StartTime: 4242, State: "S"}}
	resolve := func(context.Context, contract.ResolveOptions) (model.Manifest, error) {
		return model.Manifest{ID: "item-1"}, nil
	}
	s := New(st, p, nil, nil, resolve, nil, func() time.Time { return now }, nil)
	result, err := s.Stop(context.Background(), contract.ResolveOptions{}, true)
	if err != nil || !result.Changed || !p.terminated {
		t.Fatalf("result=%+v terminated=%v err=%v", result, p.terminated, err)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "control socket unavailable") {
		t.Fatalf("warnings=%+v", result.Warnings)
	}
}
