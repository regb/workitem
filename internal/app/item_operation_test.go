package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/regb/workitem/internal/agent"
	itemlock "github.com/regb/workitem/internal/lock"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

func TestResourceAndLifecycleOperationsShareTheItemLock(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Ensure(); err != nil {
		t.Fatal(err)
	}
	id, err := model.NewID()
	if err != nil {
		t.Fatal(err)
	}
	m := model.NewManifest(id, "lock", "Lock", nil, false, time.Now(), model.Repository{}, model.NewCheckout(model.WorkspaceKindManagedSlot, model.ExecutorLocal, "branch"))
	if err := st.CreateItem(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	a := New(st, nil)
	opts := ResolveOptions{Selector: id}
	held, err := itemlock.AcquireOperation(context.Background(), st.LockPath(id))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	cases := map[string]func(context.Context) error{
		"runtime": func(ctx context.Context) error {
			_, err := a.EnsureAgentRuntime(ctx, opts, agent.ModeHeadless)
			return err
		},
		"control": func(ctx context.Context) error {
			_, err := a.AgentControl(ctx, AgentControlOptions{ResolveOptions: opts, CommandType: agent.CommandPrompt, Message: "work"})
			return err
		},
		"stop": func(ctx context.Context) error { _, err := a.StopAgentRuntime(ctx, opts, false); return err },
		"archive": func(ctx context.Context) error {
			_, err := a.Close(ctx, opts, CloseOptions{Resolution: model.ResolutionCompleted})
			return err
		},
		"shelve": func(ctx context.Context) error { _, err := a.Shelve(ctx, opts, false); return err },
		"state": func(ctx context.Context) error {
			_, err := a.SetWorkItemState(ctx, opts, model.StateClosed, false)
			return err
		},
		"ensure":         func(ctx context.Context) error { _, err := a.EnsureWorkItemWorkspace(ctx, opts); return err },
		"release":        func(ctx context.Context) error { _, err := a.ReleaseWorkItemWorkspace(ctx, opts); return err },
		"terminal":       func(ctx context.Context) error { _, err := a.EnsureTerminal(ctx, opts); return err },
		"terminal-close": func(ctx context.Context) error { _, err := a.CloseTerminal(ctx, opts); return err },
		"delete": func(ctx context.Context) error {
			_, err := a.DeleteWorkItems(ctx, DeleteWorkItemsOptions{ResolveOptions: opts})
			return err
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			if err := call(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("operation bypassed held lock: %v", err)
			}
		})
	}
	after, err := st.LoadManifest(id)
	if err != nil || after.State != m.State || after.Checkout.Claim != nil {
		t.Fatalf("blocked operation changed item %+v %v", after, err)
	}
}
