package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/executor"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

type unavailableExecutor struct{ absent bool }

func (f *unavailableExecutor) Run(_ context.Context, args []string, _ io.Reader, out, stderr io.Writer) error {
	if f.absent && len(args) > 1 && args[0] == "container" && args[1] == "ls" {
		return nil
	}
	return errors.New("Docker unavailable")
}

func TestExecutorFailedProvisioningRetainsOneHostClaim(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	a := New(st, nil)
	a.MasterID = "test"
	a.ExecutorsConfig = map[string]executor.Profile{"dev": {Driver: "docker", Image: "dev"}}
	fake := &unavailableExecutor{}
	a.ExecutorTransport = fake
	m := model.NewManifest("01KZYHGDCECSFS4BJ2SNTQP49V", "executor", "Executor", nil, false, time.Now(), model.Repository{}, model.NewCheckout(model.WorkspaceKindExecutor, "dev", "wi/executor"))
	if err := st.CreateItem(ctx, m); err != nil {
		t.Fatal(err)
	}
	opts := ResolveOptions{Selector: m.ID}
	if _, err := a.CheckoutCreate(ctx, opts); err == nil {
		t.Fatal("local checkout create accepted executor")
	}
	if _, err := a.CheckoutRemove(ctx, opts, true); err == nil {
		t.Fatal("local checkout remove accepted executor")
	}
	if _, err := a.CheckoutStatus(ctx, opts); err == nil {
		t.Fatal("local checkout status inspected executor path on host")
	}
	first, _, err := a.ensureExecutorWorkspace(ctx, m)
	if err == nil {
		t.Fatal("expected provisioning error")
	}
	saved, err := st.LoadManifest(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Checkout.Claim == nil || saved.Checkout.Claim.Executor.Ready || saved.Checkout.Claim.ID != first.Checkout.Claim.ID {
		t.Fatalf("claim not retained: %+v", saved.Checkout)
	}
	second, _, err := a.ensureExecutorWorkspace(ctx, m)
	if err == nil {
		t.Fatal("expected provisioning error")
	}
	if second.Checkout.Claim.ID != first.Checkout.Claim.ID || second.Checkout.Claim.Generation != 1 {
		t.Fatal("retry minted another claim")
	}
	fake.absent = true
	released, err := a.releaseExecutor(ctx, second, false)
	if err != nil {
		t.Fatal(err)
	}
	if released.Checkout.Claim != nil || released.Checkout.ClaimGeneration != 1 {
		t.Fatalf("release %+v", released.Checkout)
	}
}

func TestExecutorTerminalUsesHostCWDAndExecutorShell(t *testing.T) {
	st := store.New(t.TempDir())
	a := New(st, nil)
	a.SelfPath = "/host/wi"
	m := model.Manifest{ID: "item", Checkout: model.Checkout{Specification: model.CheckoutSpecification{Kind: model.WorkspaceKindExecutor, ExecutorProfile: "dev"}, Claim: &model.WorkspaceClaim{ExecutorProfile: "dev", Path: "/workspace"}}}
	spec := a.tmuxSpecWithEnvironmentAndScrub(m, nil, nil)
	if spec.CWD != st.ItemDir(m.ID) || strings.Join(spec.ShellCommand, " ") != "/host/wi internal-executor-attach item" {
		t.Fatalf("spec=%+v", spec)
	}
}

func TestExecutorAgentRequiresBundledPi(t *testing.T) {
	m := model.Manifest{Agent: "custom", Checkout: model.Checkout{Claim: &model.WorkspaceClaim{ExecutorProfile: "dev"}}}
	a := &App{AgentsConfig: map[string]config.AgentDefinitionConfig{"custom": {Driver: "custom"}}, DriversConfig: map[string]config.DriverDefinitionConfig{"custom": {Command: "/custom"}}}
	if err := a.validateExecutorAgent(m); err == nil {
		t.Fatal("unsupported driver accepted")
	}
	m.Agent = "default"
	a.AgentsConfig = map[string]config.AgentDefinitionConfig{"default": {Driver: "pi"}}
	a.DriversConfig = map[string]config.DriverDefinitionConfig{"pi": {}}
	if err := a.validateExecutorAgent(m); err != nil {
		t.Fatal(err)
	}
	a.DriversConfig["pi"] = config.DriverDefinitionConfig{Command: "/external-pi-driver"}
	if err := a.validateExecutorAgent(m); err == nil {
		t.Fatal("external driver accepted")
	}
}
