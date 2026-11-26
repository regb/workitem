package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

// Shelving keeps the checkout assigned whenever it cannot close the item's
// terminal safely, and checks the checkout before it closes anything.
func TestShelveKeepsTheCheckoutWhenItCannotCloseTheTerminal(t *testing.T) {
	tmuxEnv := map[string]string{"TMUX": "/tmp/tmux"}
	tests := []struct {
		name string
		// arrange runs after the start, with the item's terminal session.
		arrange func(fg *fakeGit, ft *fakeTmux, session string)
		env     map[string]string
		force   bool
		refusal string
		state   string
		killed  int
		warning string
	}{
		{
			name:    "dirty checkout before the terminal",
			arrange: func(fg *fakeGit, _ *fakeTmux, _ string) { fg.status = " M file.go" },
			refusal: "uncommitted changes",
			state:   model.StateActive,
		},
		{
			name:    "terminal close fails",
			arrange: func(_ *fakeGit, ft *fakeTmux, _ string) { ft.killErr = errors.New("tmux refused") },
			refusal: "could not close tmux session",
			state:   model.StateActive,
			killed:  1,
		},
		{
			name:    "from inside the terminal",
			arrange: func(_ *fakeGit, ft *fakeTmux, session string) { ft.current = session },
			env:     tmuxEnv,
			refusal: "cannot close current tmux session",
			state:   model.StateActive,
		},
		{
			name:    "forced from inside the terminal",
			arrange: func(_ *fakeGit, ft *fakeTmux, session string) { ft.current = session },
			env:     tmuxEnv,
			force:   true,
			state:   model.StateBacklog,
			warning: "left current terminal",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := store.New(t.TempDir())
			fg := newFakeGit(t)
			ft := &fakeTmux{}
			application := newTestApp(st, fg)
			application.Tmux = ft
			application.Clock = fixedClock{testutil.Time()}
			started, _ := startItem(t, application, app.NewWorkItemOptions{Title: "Shelve"}, agent.ModeTUI)
			session := started.TerminalSessionName()
			tc.arrange(fg, ft, session)

			res, err := application.Shelve(context.Background(), app.ResolveOptions{Selector: started.ID, Env: tc.env}, tc.force)
			if tc.refusal != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refusal) {
					t.Fatalf("err = %v, want %q", err, tc.refusal)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			m, err := st.LoadManifest(started.ID)
			if err != nil {
				t.Fatal(err)
			}
			if m.State != tc.state || !m.Checkout.Present() || m.Checkout.Claim == nil {
				t.Fatalf("checkout should remain assigned: %+v", m)
			}
			if len(ft.killed) != tc.killed || (tc.killed > 0 && ft.killed[0] != session) {
				t.Fatalf("killed = %+v", ft.killed)
			}
			if tc.warning != "" && !strings.Contains(strings.Join(res.Warnings, "\n"), tc.warning) {
				t.Fatalf("warnings = %+v, want %q", res.Warnings, tc.warning)
			}
		})
	}
}
