package app_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/app"
	viewapp "github.com/regb/workitem/internal/app/view"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

type navigationStore struct {
	app.Store
	historyReads   int
	failReadMarker bool
}

func (s *navigationStore) ReadEvents(string) ([]model.Event, error) {
	s.historyReads++
	return nil, fmt.Errorf("navigation must not fetch the full event history")
}
func (s *navigationStore) AppendEvent(ctx context.Context, id string, event model.Event) error {
	if s.failReadMarker && event.Type == "attention.read" {
		return fmt.Errorf("injected read-marker write failure")
	}
	return s.Store.AppendEvent(ctx, id, event)
}

type navigationTmux struct {
	fakeTmux
	disabled bool
}

func (m *navigationTmux) SetPaneInput(_ context.Context, _ string, enabled bool) error {
	m.disabled = !enabled
	return nil
}
func (m *navigationTmux) PaneInputDisabled(context.Context, string) (bool, error) {
	return m.disabled, nil
}
func (m *navigationTmux) SessionClients(context.Context, string) ([]string, error) {
	return []string{"terminal-client"}, nil
}
func (m *navigationTmux) DisplayMessage(context.Context, string, string) error { return nil }

type navigationWebLease struct{ lease model.ControlLease }

func (l navigationWebLease) Lease(context.Context, string) (*model.ControlLease, error) {
	copy := l.lease
	return &copy, nil
}
func (navigationWebLease) AcquireLease(context.Context, model.LeaseRequest) (model.LeaseResult, error) {
	return model.LeaseResult{}, fmt.Errorf("read-only navigation must not acquire a lease")
}
func (navigationWebLease) ReleaseLease(context.Context, model.LeaseRequest) (model.LeaseResult, error) {
	return model.LeaseResult{}, fmt.Errorf("read-only navigation must not release a lease")
}

func TestNavigationRecordsReadWithoutFetchingActivityDetails(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		for _, failMarker := range []bool{false, true} {
			t.Run(fmt.Sprintf("read-only=%t/write-fails=%t", readOnly, failMarker), func(t *testing.T) {
				ctx := context.Background()
				native := store.New(t.TempDir())
				a := newTestApp(native, newFakeGit(t))
				mux := &navigationTmux{}
				a.Tmux = mux
				created, err := a.NewWorkItem(ctx, app.NewWorkItemOptions{Title: "Fast popup handoff", CWD: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				opts := app.ResolveOptions{Selector: created.Manifest.ID}
				if _, err := a.StartWorkItem(ctx, opts, false, false); err != nil {
					t.Fatal(err)
				}
				a.Process = &fakeProcess{alive: map[int]bool{4321: true}}
				runtime := model.AgentRuntime{ID: "running-navigation-target", WorkItemID: created.Manifest.ID, Mode: "tui", State: model.AgentRuntimeRunning, HostPID: 4321, HostProcessGroup: 4321, HostStartTime: 4321, Capabilities: model.RuntimeCapabilities{NativeTUI: true}}
				if err := native.SaveAgentRuntime(ctx, created.Manifest.ID, runtime); err != nil {
					t.Fatal(err)
				}
				if readOnly {
					a.Leases = navigationWebLease{lease: model.ControlLease{ID: "web-lease", ItemID: created.Manifest.ID, Holder: model.LeaseHolderWeb, ClientID: "browser"}}
				}
				a.WorkListObserver = func(context.Context, string) (viewapp.Observation, error) {
					return viewapp.Observation{Status: "idle"}, nil
				}
				conversationReads := 0
				a.ConversationProjectionObserver = func(context.Context, model.Manifest, time.Time) (app.ConversationStatus, []string) {
					conversationReads++
					return app.ConversationStatus{}, nil
				}
				observed := &navigationStore{Store: native, failReadMarker: failMarker}
				a.Store = observed
				result, err := a.SwitchWorkItem(ctx, opts, true)
				if err != nil {
					t.Fatal(err)
				}
				if result.ReadOnly != readOnly || result.Terminal == nil || !result.Terminal.Attached {
					t.Fatal("did not enter the requested terminal", result)
				}
				if readOnly && !mux.disabled {
					t.Fatal("read-only navigation enabled pane input")
				}
				if observed.historyReads != 0 || conversationReads != 0 {
					t.Fatalf("post-switch enrichment delayed popup completion: history=%d conversation=%d", observed.historyReads, conversationReads)
				}
				events, err := native.ReadEvents(created.Manifest.ID)
				if err != nil {
					t.Fatal(err)
				}
				marked := 0
				for _, event := range events {
					if event.Type == "attention.read" {
						marked++
					}
				}
				if failMarker {
					if marked != 0 || !strings.Contains(strings.Join(result.Warnings, "\n"), "injected read-marker write failure") {
						t.Fatal("read-marker failure was hidden", result.Warnings)
					}
				} else if marked != 1 {
					t.Fatalf("navigation did not durably mark the item read exactly once: %d", marked)
				}
			})
		}
	}
}

func TestSwitchingAwayMarksTheLeftItemRead(t *testing.T) {
	for _, markedUnread := range []bool{false, true} {
		t.Run(fmt.Sprintf("marked-unread=%t", markedUnread), func(t *testing.T) {
			ctx := context.Background()
			native := store.New(t.TempDir())
			a := newTestApp(native, newFakeGit(t))
			a.Tmux = &navigationTmux{}
			var ids []string
			for _, title := range []string{"Watched reply", "Next item"} {
				created, err := a.NewWorkItem(ctx, app.NewWorkItemOptions{Title: title, CWD: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := a.StartWorkItem(ctx, app.ResolveOptions{Selector: created.Manifest.ID}, false, false); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, created.Manifest.ID)
			}
			a.WorkListObserver = func(context.Context, string) (viewapp.Observation, error) {
				return viewapp.Observation{Status: "idle"}, nil
			}
			read, unread := time.Now().Add(-time.Minute), time.Now()
			a.AttentionActivityReader = func(context.Context, string) (app.AttentionActivity, error) {
				if markedUnread {
					return app.AttentionActivity{LastReadAt: &read, LastUnreadAt: &unread}, nil
				}
				return app.AttentionActivity{}, nil
			}
			env := map[string]string{"TMUX": "/tmp/tmux-test,1,0", "WI_ID": ids[0]}
			if _, err := a.SwitchWorkItem(ctx, app.ResolveOptions{Selector: ids[1], Env: env}, true); err != nil {
				t.Fatal(err)
			}
			events, err := native.ReadEvents(ids[0])
			if err != nil {
				t.Fatal(err)
			}
			marked := 0
			for _, event := range events {
				if event.Type == "attention.read" {
					marked++
				}
			}
			if want := map[bool]int{false: 1, true: 0}[markedUnread]; marked != want {
				t.Fatalf("left item read marks = %d, want %d", marked, want)
			}
		})
	}
}
