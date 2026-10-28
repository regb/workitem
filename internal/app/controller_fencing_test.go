package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/regb/workitem/internal/agent"
	viewapp "github.com/regb/workitem/internal/app/view"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/process"
	"github.com/regb/workitem/internal/testutil"
	"os"
	"strings"
	"testing"
	"time"
)

type controllerTestLeases struct {
	current      *model.ControlLease
	race         bool
	acquisitions int
}

func (l *controllerTestLeases) Lease(_ context.Context, id string) (*model.ControlLease, error) {
	if l.race {
		l.current = &model.ControlLease{ID: "concurrent", ItemID: id, Holder: model.LeaseHolderWeb, ClientID: "other"}
		return nil, nil
	}
	if l.current == nil {
		return nil, nil
	}
	copy := *l.current
	return &copy, nil
}
func (l *controllerTestLeases) AcquireLease(_ context.Context, r model.LeaseRequest) (model.LeaseResult, error) {
	actual := ""
	if l.current != nil {
		actual = l.current.ID
	}
	if r.ExpectedLeaseID != nil && *r.ExpectedLeaseID != actual || l.current != nil && !l.current.SameClient(r.Holder, r.ClientID) && !r.Takeover {
		return model.LeaseResult{Lease: l.current}, nil
	}
	l.acquisitions++
	id := fmt.Sprint(l.acquisitions)
	if l.current != nil && l.current.SameClient(r.Holder, r.ClientID) {
		id = l.current.ID
	}
	l.current = &model.ControlLease{ID: id, ItemID: r.ItemID, Holder: r.Holder, ClientID: r.ClientID}
	return model.LeaseResult{Granted: true, Lease: l.current}, nil
}
func (l *controllerTestLeases) ReleaseLease(_ context.Context, r model.LeaseRequest) (model.LeaseResult, error) {
	if l.current == nil || r.ExpectedLeaseID != nil && *r.ExpectedLeaseID != l.current.ID || !l.current.SameClient(r.Holder, r.ClientID) {
		return model.LeaseResult{}, nil
	}
	l.current = nil
	return model.LeaseResult{Granted: true}, nil
}
func TestAcquireControlDoesNotStealAfterConcurrentAcquisition(t *testing.T) {
	a, _, m := newCoreTestApp(t, time.Now(), model.StateActive, false)
	leases := &controllerTestLeases{race: true}
	a.Leases = leases
	r, err := a.AcquireControl(context.Background(), ControlRequest{ResolveOptions: ResolveOptions{Selector: m.ID}, Holder: model.LeaseHolderWeb, ClientID: "initial"})
	if err == nil || r.Granted || leases.current.ClientID != "other" {
		t.Fatal("unconfirmed race displaced another client", r, err)
	}
}
func TestStructuredControlRequiresCurrentControllerAndLeaseGeneration(t *testing.T) {
	a, st, m := newCoreTestApp(t, time.Now(), model.StateActive, false)
	a.Process = process.New()
	info, err := a.Process.Info(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	a.AgentRuntimeSocketRoot = testutil.ShortTempDir(t)
	runtime := model.AgentRuntime{ID: "test-control", WorkItemID: m.ID, HostPID: info.PID, HostProcessGroup: info.PGRP, HostStartTime: info.StartTime, Mode: "headless", Capabilities: model.RuntimeCapabilities{Prompt: true}}
	if err := st.SaveAgentRuntime(context.Background(), m.ID, runtime); err != nil {
		t.Fatal(err)
	}
	if err := a.stopRuntimeForReplacement(context.Background(), m, "superseded-runtime", true); err == nil {
		t.Fatal("takeover stopped an unplanned runtime owner")
	}
	server, err := agent.ListenControlSocket(a.runtimeControlSocketPath(m.ID, &runtime))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	seen := make(chan string, 4)
	go func() {
		for req := range server.Requests() {
			seen <- req.Command.Type
			req.Respond(nil)
		}
	}()
	leases := &controllerTestLeases{current: &model.ControlLease{ID: "lease-current", ItemID: m.ID, Holder: model.LeaseHolderWeb, ClientID: "browser"}}
	a.Leases = leases
	opts := AgentControlOptions{ResolveOptions: ResolveOptions{Selector: m.ID}, CommandType: agent.CommandPrompt, Message: "work"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := a.AgentControl(ctx, opts); err == nil {
		t.Fatal("CLI prompt bypassed another controller")
	}
	opts.ControllerHolder = model.LeaseHolderWeb
	opts.ControllerClientID = "browser"
	opts.ControllerLeaseID = "old-lease"
	if _, err := a.AgentControl(ctx, opts); err == nil {
		t.Fatal("stale generation sent input")
	}
	select {
	case <-seen:
		t.Fatal("rejected control reached driver")
	default:
	}
	opts.ControllerLeaseID = "lease-current"
	if result, err := a.AgentControl(ctx, opts); err != nil || !result.Submitted {
		t.Fatal(result, err)
	}
	if <-seen != agent.CommandPrompt {
		t.Fatal("wrong command")
	}
	opts.ControllerHolder = ""
	opts.ControllerClientID = ""
	opts.ControllerLeaseID = ""
	leases.current = nil
	if result, err := a.AgentControl(ctx, opts); err != nil || !result.Submitted {
		t.Fatal(result, err)
	}
	if leases.current != nil {
		t.Fatal("one-shot CLI submission retained a lease")
	}
}

// validConfigBackend accepts any agent configuration.
type validConfigBackend struct{ ConversationBackend }

func (validConfigBackend) ValidateConfig(context.Context, string, json.RawMessage) error { return nil }

// A confirmed takeover that replaces the runtime aborts the agent's turn even
// when the plan saw it idle: the daemon's observation can lag the agent, and
// wi switch --take promises to abort a turn in flight.
func TestConfirmedTakeoverAbortsWhenReplacingTheRuntime(t *testing.T) {
	a, st, m := newCoreTestApp(t, time.Now(), model.StateActive, false)
	a.Process = process.New()
	info, err := a.Process.Info(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	a.AgentRuntimeSocketRoot = testutil.ShortTempDir(t)
	a.ArchiveRuntimeStopTimeout = 50 * time.Millisecond
	a.DefaultAgent = "pi"
	a.AgentsConfig = map[string]config.AgentDefinitionConfig{"pi": {Driver: "pi"}}
	a.ConversationBackends["pi"] = validConfigBackend{}
	a.WorkListObserver = func(context.Context, string) (viewapp.Observation, error) {
		return viewapp.Observation{Status: "idle"}, nil
	}
	a.Leases = &controllerTestLeases{}
	runtime := model.AgentRuntime{ID: "headless-1", WorkItemID: m.ID, HostPID: info.PID, HostProcessGroup: info.PGRP, HostStartTime: info.StartTime, Mode: string(agent.ModeHeadless), Capabilities: model.RuntimeCapabilities{Prompt: true, Cancel: true}}
	if err := st.SaveAgentRuntime(context.Background(), m.ID, runtime); err != nil {
		t.Fatal(err)
	}
	server, err := agent.ListenControlSocket(a.runtimeControlSocketPath(m.ID, &runtime))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	seen := make(chan string, 4)
	go func() {
		for req := range server.Requests() {
			seen <- req.Command.Type
			req.Respond(nil)
		}
	}()
	result, err := a.AcquireControl(context.Background(), ControlRequest{ResolveOptions: ResolveOptions{Selector: m.ID}, Holder: model.LeaseHolderTerminal, ClientID: "terminal", Confirm: true})
	if result.Plan.Replace != string(agent.ModeTUI) || result.Plan.Abort {
		t.Fatalf("plan = %+v, want a replacement planned without an abort", result.Plan)
	}
	// This process is the runtime and never exits; the stop must get past
	// the busy check and abort first.
	if err == nil || !strings.Contains(err.Error(), "did not exit") {
		t.Fatalf("takeover err = %v, want the replacement to wait for the runtime", err)
	}
	if first := <-seen; first != agent.CommandAbort {
		t.Fatalf("first command = %q, want abort", first)
	}
}
