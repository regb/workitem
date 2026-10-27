package app_test

import (
	"context"
	"testing"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

type forbiddenArchiveTmux struct {
	fakeTmux
	t *testing.T
}

func (f *forbiddenArchiveTmux) HasSession(context.Context, string) (bool, error) {
	f.t.Error("headless-only archive inspected tmux")
	return false, nil
}
func TestHeadlessOnlyArchiveDoesNotInspectTmux(t *testing.T) {
	st := store.New(t.TempDir())
	application := newTestApp(st, newFakeGit(t))
	application.Tmux = &forbiddenArchiveTmux{t: t}
	id := testutil.ID(t, 223)
	application.NewID = func() (string, error) { return id, nil }
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "No terminal", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.EnsureWorkItemWorkspace(context.Background(), app.ResolveOptions{Selector: id}); err != nil {
		t.Fatal(err)
	}
	runtime := model.AgentRuntime{ID: "stopped-headless", WorkItemID: id, Mode: "headless"}
	if err := st.SaveAgentRuntime(context.Background(), id, runtime); err != nil {
		t.Fatal(err)
	}
	result, err := application.Close(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID, Env: map[string]string{"TMUX": "caller-pane"}}, app.CloseOptions{Resolution: model.ResolutionCompleted})
	if err != nil || result.State != model.StateClosed || result.Manifest.Checkout.Claim != nil {
		t.Fatalf("archive %+v %v", result, err)
	}
}
