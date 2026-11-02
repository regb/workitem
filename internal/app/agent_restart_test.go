package app

import (
	"context"
	"os"
	"strings"
	"testing"

	viewapp "github.com/regb/workitem/internal/app/view"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/process"
	"github.com/regb/workitem/internal/store"
)

func TestRestartAgentNeedsARunningIdleAgent(t *testing.T) {
	a, _, m := settingsApp(t)
	ctx := context.Background()
	if _, err := a.RestartAgent(ctx, ResolveOptions{Selector: m.ID}); err == nil || !strings.Contains(err.Error(), "no agent is running") {
		t.Fatalf("restarted without a runtime: %v", err)
	}
	if agents := a.RepositoryAgents(ctx, m.Repository.ID); len(agents) != 0 {
		t.Fatalf("listed agents without a runtime: %+v", agents)
	}

	a.Process = process.New()
	info, err := a.Process.Info(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	runtime := model.AgentRuntime{ID: "live", WorkItemID: m.ID, HostPID: info.PID, HostProcessGroup: info.PGRP, HostStartTime: info.StartTime, Mode: "headless"}
	if err := a.Store.(*store.Store).SaveAgentRuntime(ctx, m.ID, runtime); err != nil {
		t.Fatal(err)
	}
	a.WorkListObserver = func(context.Context, string) (viewapp.Observation, error) {
		return viewapp.Observation{Status: "busy"}, nil
	}
	agents := a.RepositoryAgents(ctx, m.Repository.ID)
	if len(agents) != 1 || agents[0].WorkItemID != m.ID || !agents[0].Busy || agents[0].Mode != "headless" {
		t.Fatalf("running agents: %+v", agents)
	}
	if other := a.RepositoryAgents(ctx, "another-repository"); len(other) != 0 {
		t.Fatalf("listed another repository's agents: %+v", other)
	}
	if _, err := a.RestartAgent(ctx, ResolveOptions{Selector: m.ID}); err == nil || !strings.Contains(err.Error(), "working") {
		t.Fatalf("restarted a busy agent: %v", err)
	}
}
