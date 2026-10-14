package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

type configCheckingBackend struct {
	fakeConversationBackend
	calls  int
	name   string
	values map[string]any
}

func (b *configCheckingBackend) ValidateConfig(_ context.Context, name string, raw json.RawMessage) error {
	b.calls++
	b.name = name
	if err := json.Unmarshal(raw, &b.values); err != nil {
		return err
	}
	if b.values["required"] != "present" {
		return fmt.Errorf("required setting is missing")
	}
	return nil
}

func TestDefaultAgentIsResolvedAndStoredAtCreation(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	a := newTestApp(st, newFakeGit(t))
	first, err := a.NewWorkItem(ctx, app.NewWorkItemOptions{Title: "First", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.Agent != "pi" {
		t.Fatalf("stored agent %q", first.Manifest.Agent)
	}
	a.AgentsConfig["review"] = config.AgentDefinitionConfig{Driver: "pi", Config: map[string]any{}}
	a.DefaultAgent = "review"
	second, err := a.NewWorkItem(ctx, app.NewWorkItemOptions{Title: "Second", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if second.Manifest.Agent != "review" {
		t.Fatalf("stored agent %q", second.Manifest.Agent)
	}
	original, err := st.LoadManifest(first.Manifest.ID)
	if err != nil || original.Agent != "pi" {
		t.Fatalf("existing item changed: %+v %v", original, err)
	}
	explicit, err := a.NewWorkItem(ctx, app.NewWorkItemOptions{Title: "Explicit", Agent: "pi", CWD: t.TempDir()})
	if err != nil || explicit.Manifest.Agent != "pi" {
		t.Fatalf("explicit: %+v %v", explicit, err)
	}
}

func TestAgentConfigValidationMergesSettingsWithoutResources(t *testing.T) {
	st := store.New(t.TempDir())
	a := newTestApp(st, newFakeGit(t))
	backend := &configCheckingBackend{}
	a.ConversationBackends["pi"] = backend
	a.DriversConfig["pi"] = config.DriverDefinitionConfig{Defaults: map[string]any{"required": "present", "setting": "default"}}
	a.AgentsConfig["review"] = config.AgentDefinitionConfig{Driver: "pi", Config: map[string]any{"setting": "override"}}
	if err := a.ValidateAgentConfig(context.Background(), "review"); err != nil {
		t.Fatal(err)
	}
	if backend.calls != 1 || backend.name != "review" || backend.values["setting"] != "override" {
		t.Fatalf("validation=%+v", backend)
	}
	if a.DriversConfig["pi"].Defaults["setting"] != "default" {
		t.Fatal("validation mutated defaults")
	}
	if _, err := st.LoadManifest("missing"); err == nil {
		t.Fatal("validation created an item")
	}
	a.ConversationBackends = nil
	if err := a.ValidateAgentConfig(context.Background(), "review"); err == nil {
		t.Fatal("missing validator silently accepted")
	}
}

func TestInvalidAgentConfigFailsBeforeStartupEffects(t *testing.T) {
	for _, mode := range []agent.Mode{agent.ModeTUI, agent.ModeHeadless} {
		for _, action := range []string{"ensure", "start", "resume", "new"} {
			t.Run(string(mode)+"/"+action, func(t *testing.T) {
				ctx := context.Background()
				st := store.New(t.TempDir())
				git := newFakeGit(t)
				a := newTestApp(st, git)
				backend := &configCheckingBackend{}
				a.ConversationBackends["pi"] = backend
				tmux := &fakeTmux{}
				a.Tmux = tmux
				if action == "new" {
					_, err := a.NewAgentWorkItem(ctx, app.NewWorkItemOptions{Title: "Invalid config", CWD: t.TempDir()}, mode, "task", "operator")
					if err == nil || !strings.Contains(err.Error(), "required setting") {
						t.Fatalf("err=%v", err)
					}
					items, _ := st.ListManifests()
					if len(items) != 0 {
						t.Fatal("invalid config created an item")
					}
				} else {
					created, err := a.NewWorkItem(ctx, app.NewWorkItemOptions{Title: "Invalid config", CWD: t.TempDir()})
					if err != nil {
						t.Fatal(err)
					}
					m := created.Manifest
					if action == "ensure" {
						m.State = model.StateWorking
					}
					if action == "resume" {
						m.State = model.StateWaiting
					}
					if err := st.SaveManifest(ctx, m); err != nil {
						t.Fatal(err)
					}
					opts := app.ResolveOptions{Selector: m.ID}
					switch action {
					case "ensure":
						_, err = a.EnsureAgentRuntime(ctx, opts, mode)
					case "start":
						_, err = a.StartWorkItemInMode(ctx, opts, false, false, mode)
					case "resume":
						_, err = a.ResumeWorkItemInMode(ctx, opts, false, mode)
					}
					if err == nil || !strings.Contains(err.Error(), "required setting") {
						t.Fatalf("err=%v", err)
					}
					after, err := st.LoadManifest(m.ID)
					if err != nil || after.State != m.State || after.Checkout.Claim != nil || after.PrimaryConversation != nil {
						t.Fatalf("startup changed item: %+v %v", after, err)
					}
					runtime, err := st.LoadAgentRuntime(m.ID)
					if err != nil || runtime != nil {
						t.Fatalf("startup created runtime: %+v %v", runtime, err)
					}
				}
				if len(git.worktrees) != 0 || len(git.branches) != 0 || len(tmux.ensured) != 0 || len(tmux.launched) != 0 {
					t.Fatal("invalid config allocated external resources")
				}
				if backend.calls != 1 {
					t.Fatalf("validation calls=%d", backend.calls)
				}
			})
		}
	}
}

func TestExistingOwnerDoesNotValidateNewProfileSettings(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	a := newTestApp(st, newFakeGit(t))
	created, err := a.NewWorkItem(ctx, app.NewWorkItemOptions{Title: "Existing owner", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	a.Process = &fakeProcess{alive: map[int]bool{123: true}}
	runtime := model.AgentRuntime{ID: "owner", WorkItemID: created.Manifest.ID, State: model.AgentRuntimeRunning, Mode: "headless", HostPID: 123, HostProcessGroup: 123, HostStartTime: 123}
	if err := st.SaveAgentRuntime(ctx, created.Manifest.ID, runtime); err != nil {
		t.Fatal(err)
	}
	backend := &configCheckingBackend{}
	a.ConversationBackends["pi"] = backend
	if err := a.ValidateItemAgentConfig(ctx, app.ResolveOptions{Selector: created.Manifest.ID}); err != nil {
		t.Fatal(err)
	}
	if backend.calls != 0 {
		t.Fatal("existing owner was blocked by new config")
	}
}
