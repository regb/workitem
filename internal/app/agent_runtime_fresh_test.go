package app

import (
	"context"
	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"testing"
	"time"
)

func TestQueuedStartupGuardPrecedesWorkspaceOrTerminalWork(t *testing.T) {
	for _, mode := range []agent.Mode{agent.ModeTUI, agent.ModeHeadless} {
		for _, state := range []string{model.StateBacklog, model.StateClosed, model.StateActive} {
			t.Run(string(mode)+"/"+state, func(t *testing.T) {
				st := store.New(t.TempDir())
				if err := st.Ensure(); err != nil {
					t.Fatal(err)
				}
				id, _ := model.NewID()
				m := model.NewManifest(id, "guard", "Guard", nil, false, time.Now(), model.Repository{}, model.NewCheckout(model.WorkspaceKindManagedSlot, model.ExecutorLocal, "branch"))
				m.State = state
				if state == model.StateClosed {
					m.Slug = ""
				}
				if state == model.StateActive {
					m.PrimaryConversation = &model.Conversation{ID: "already-created", Driver: "pi", Locator: "sessions/existing"}
				}
				if err := st.CreateItem(context.Background(), m); err != nil {
					t.Fatal(err)
				}
				a := New(st, nil)
				if _, err := a.EnsureNewAgentRuntime(context.Background(), ResolveOptions{Selector: id}, mode); err == nil {
					t.Fatal("queued startup touched changed work")
				}
				after, err := st.LoadManifest(id)
				if err != nil || after.Checkout.Present() || after.State != state {
					t.Fatalf("guard changed manifest: %+v %v", after, err)
				}
			})
		}
	}
}
