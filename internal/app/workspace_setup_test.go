package app_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/plugin"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

func TestAnAgentStartsOnlyOnceItsCheckoutIsSetUp(t *testing.T) {
	st := store.New(t.TempDir())
	itemID, sessionID := testutil.ID(t, 141), testutil.ID(t, 142)
	launcher := &fakeRuntimeLauncher{pid: 4321}
	application := newTestApp(st, newFakeGit(t))
	application.Tmux = &fakeTmux{}
	application.Process = &fakeProcess{alive: map[int]bool{4321: true}}
	application.AgentRuntimeLauncher = launcher
	application.SelfPath = "/usr/local/bin/wi"
	application.Clock = fixedClock{testutil.Time()}
	application.AgentRuntimeStateRoot = t.TempDir()
	ids := []string{itemID, sessionID}
	application.NewID = func() (string, error) { id := ids[0]; ids = ids[1:]; return id, nil }
	runs, broken := []plugin.SetupRequest{}, true
	application.Preparation = plugin.Pipeline{Hooks: []plugin.Hooks{{Name: "test", SetupCheckout: func(_ context.Context, request plugin.SetupRequest, _ map[string]any) ([]string, error) {
		runs = append(runs, request)
		fmt.Fprintln(request.Log, "installing")
		if broken {
			return []string{"careful"}, errors.New("install failed")
		}
		return nil, nil
	}}}}
	ctx := context.Background()
	if _, err := application.NewWorkItem(ctx, app.NewWorkItemOptions{Title: "Setup", CWD: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.SetWorkItemState(ctx, app.ResolveOptions{Selector: itemID}, model.StateActive, false); err != nil {
		t.Fatal(err)
	}
	opts := app.ResolveOptions{Selector: itemID}

	// A failed setup keeps the claim and starts no agent.
	if _, err := application.EnsureAgentRuntime(ctx, opts, agent.ModeHeadless); err == nil || !strings.Contains(err.Error(), "install failed") {
		t.Fatalf("start with broken setup = %v", err)
	}
	if len(launcher.spec.Args) != 0 {
		t.Fatalf("an agent started: %+v", launcher.spec)
	}
	m, err := st.LoadManifest(itemID)
	if err != nil || m.Checkout.Claim == nil {
		t.Fatalf("claim lost: %+v %v", m.Checkout, err)
	}
	status, err := application.WorkspaceSetupStatus(ctx, opts)
	if err != nil || !status.Current || status.Run.Status != app.SetupFailed || !strings.Contains(status.LogTail, "installing") || len(status.Run.Warnings) != 1 {
		t.Fatalf("status = %+v %v", status, err)
	}
	request := runs[0]
	if request.Subject.CheckoutPath != m.Checkout.Claim.Path || request.Environment["WI_ID"] != itemID || request.Environment["WI_SLOT_REUSED"] != "0" || request.FilesDir != st.RepositoryFilesDir(m.Repository.ID) {
		t.Fatalf("request = %+v", request)
	}

	// Once setup succeeds the agent starts, and setup is not repeated for
	// the same claim.
	broken = false
	if _, err := application.SetupWorkspace(ctx, opts, true); err != nil {
		t.Fatal(err)
	}
	if _, err := application.EnsureAgentRuntime(ctx, opts, agent.ModeHeadless); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || len(launcher.spec.Args) == 0 {
		t.Fatalf("runs = %d, launched = %+v", len(runs), launcher.spec.Args)
	}
	if _, err := application.SetupWorkspace(ctx, opts, true); err == nil || !strings.Contains(err.Error(), "agent runs") {
		t.Fatalf("rerun under a running agent = %v", err)
	}
}

func TestSkippingSetupRecordsIt(t *testing.T) {
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 143)
	application := newTestApp(st, newFakeGit(t))
	application.Clock = fixedClock{testutil.Time()}
	application.AgentRuntimeStateRoot = t.TempDir()
	application.NewID = func() (string, error) { return itemID, nil }
	ran := false
	application.Preparation = plugin.Pipeline{Hooks: []plugin.Hooks{{Name: "test", SetupCheckout: func(context.Context, plugin.SetupRequest, map[string]any) ([]string, error) {
		ran = true
		return nil, nil
	}}}}
	ctx := context.Background()
	if _, err := application.NewWorkItem(ctx, app.NewWorkItemOptions{Title: "Skip", CWD: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.SetWorkItemState(ctx, app.ResolveOptions{Selector: itemID}, model.StateActive, false); err != nil {
		t.Fatal(err)
	}
	result, err := application.SetupWorkspace(app.WithSkipSetup(ctx), app.ResolveOptions{Selector: itemID}, false)
	if err != nil || ran || result.Run.Status != app.SetupSkipped {
		t.Fatalf("skip = %+v %v ran=%v", result, err, ran)
	}
}

func TestASetupOutlivingItsCallerFinishesAndIsNotRepeated(t *testing.T) {
	st := store.New(t.TempDir())
	itemID, sessionID := testutil.ID(t, 151), testutil.ID(t, 152)
	launcher := &fakeRuntimeLauncher{pid: 4321}
	application := newTestApp(st, newFakeGit(t))
	application.Tmux = &fakeTmux{}
	application.Process = &fakeProcess{alive: map[int]bool{4321: true}}
	application.AgentRuntimeLauncher = launcher
	application.SelfPath = "/usr/local/bin/wi"
	application.Clock = fixedClock{testutil.Time()}
	application.AgentRuntimeStateRoot = t.TempDir()
	ids := []string{itemID, sessionID}
	application.NewID = func() (string, error) { id := ids[0]; ids = ids[1:]; return id, nil }
	var runs atomic.Int32
	application.Preparation = plugin.Pipeline{Hooks: []plugin.Hooks{{Name: "slow", SetupCheckout: func(context.Context, plugin.SetupRequest, map[string]any) ([]string, error) {
		runs.Add(1)
		time.Sleep(300 * time.Millisecond)
		return nil, nil
	}}}}
	ctx := context.Background()
	if _, err := application.NewWorkItem(ctx, app.NewWorkItemOptions{Title: "Slow", CWD: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.SetWorkItemState(ctx, app.ResolveOptions{Selector: itemID}, model.StateActive, false); err != nil {
		t.Fatal(err)
	}
	opts := app.ResolveOptions{Selector: itemID}
	short, cancel := context.WithTimeout(app.WithDetachedSetup(ctx), 50*time.Millisecond)
	defer cancel()
	if _, err := application.EnsureAgentRuntime(short, opts, agent.ModeHeadless); !errors.Is(err, app.ErrSetupContinues) {
		t.Fatalf("short start = %v", err)
	}
	if ready, err := application.WaitWorkspaceSetup(ctx, opts); err != nil || !ready {
		t.Fatalf("wait = %v %v", ready, err)
	}
	if _, err := application.EnsureAgentRuntime(ctx, opts, agent.ModeHeadless); err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 1 || len(launcher.spec.Args) == 0 {
		t.Fatalf("runs=%d launched=%v", runs.Load(), launcher.spec.Args)
	}
}
