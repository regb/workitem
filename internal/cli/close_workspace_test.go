package cli_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
)

func TestCoordinatorCloseReleasesWorkspaceBeforeClosing(t *testing.T) {
	for _, next := range []bool{false, true} {
		for _, resolution := range []string{model.ResolutionCompleted, model.ResolutionAbandoned, model.ResolutionSuperseded} {
			t.Run(fmt.Sprintf("next=%t/%s", next, resolution), func(t *testing.T) {
				_, _, a := configuredApp(t)
				ctx := context.Background()
				created, err := a.NewWorkItem(ctx, app.NewWorkItemOptions{Title: "Close claimed workspace", CWD: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				opts := app.ResolveOptions{Selector: created.Manifest.ID}
				if _, err := a.SetWorkItemState(ctx, opts, model.StateActive, false); err != nil {
					t.Fatal(err)
				}
				workspace, err := a.EnsureWorkItemWorkspace(ctx, opts)
				if err != nil {
					t.Fatal(err)
				}
				if workspace.Manifest.Checkout.Claim == nil {
					t.Fatal("fixture has no checkout claim")
				}
				if _, err := a.EnsureTerminal(ctx, opts); err != nil {
					t.Fatal(err)
				}
				if _, err := a.AddBlockers(ctx, opts, []model.Blocker{{Kind: model.BlockerExternal, Event: "review"}}); err != nil {
					t.Fatal(err)
				}
				var result app.StateTransitionResult
				var errClose error
				if next {
					successor, err := a.NewWorkItem(ctx, app.NewWorkItemOptions{Title: "Successor", CWD: t.TempDir()})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := a.StartWorkItem(ctx, app.ResolveOptions{Selector: successor.Manifest.ID}, false, false); err != nil {
						t.Fatal(err)
					}
					queue := app.AttentionQueueResult{Candidates: []app.AttentionCandidate{{Item: app.WorkListItem{ID: successor.Manifest.ID}}}}
					navigated, err := a.NextWorkItem(ctx, app.NextWorkItemOptions{ResolveOptions: opts, CloseCurrent: true, CloseResolution: resolution, PrecomputedQueue: &queue}, true)
					errClose = err
					if navigated.Closed != nil {
						result = *navigated.Closed
					}
					if err == nil && navigated.Selected.Item.ID != successor.Manifest.ID {
						t.Fatal("did not select successor")
					}
				} else {
					result, errClose = a.Close(ctx, opts, resolution, false)
				}
				if errClose != nil {
					t.Fatal(errClose)
				}
				retained, err := a.Store.LoadManifest(created.Manifest.ID)
				if err != nil {
					t.Fatal(err)
				}
				if result.State != model.StateClosed || retained.State != model.StateClosed || retained.Resolution != resolution || retained.Checkout.Claim != nil || retained.Slug != "" || len(retained.Blockers) != 0 {
					t.Fatalf("close did not commit cleanup and resolution: %+v", retained)
				}
				events, err := a.Store.ReadEvents(created.Manifest.ID)
				if err != nil {
					t.Fatal(err)
				}
				released, cleared := false, false
				for _, event := range events {
					released = released || event.Type == "workspace.released"
					cleared = cleared || (event.Type == "blocker.cleared" && event.Data["cause"] == "closed")
				}
				if !released || !cleared {
					t.Fatal("close lost workspace release or blocker clearance history")
				}
				if retained.Title != created.Manifest.Title || retained.Checkout.Specification.Branch != workspace.Manifest.Checkout.Specification.Branch || retained.Checkout.ClaimGeneration != workspace.Manifest.Checkout.ClaimGeneration {
					t.Fatal("close changed durable identity or checkout specification")
				}
			})
		}
	}
}
