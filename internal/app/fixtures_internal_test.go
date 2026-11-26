package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pidriver "github.com/regb/workitem/internal/agentdriver/pi"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

// Fixtures for the package-internal tests: an app on the native store with
// one item and no resource adapters.

type coreTestClock struct{ now time.Time }

func (c coreTestClock) Now() time.Time { return c.now }

func newCoreTestApp(t *testing.T, now time.Time, state string, deep bool) (*App, *store.Store, model.Manifest) {
	t.Helper()
	st := store.New(t.TempDir())
	if err := st.Ensure(); err != nil {
		t.Fatal(err)
	}
	manifest := coreManifest(t, testutil.ID(t, 30), "core-item", state, deep, now.Add(-time.Hour))
	if err := st.CreateItem(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	application := New(st, nil)
	application.ConversationProjectionObserver = pidriver.ObserveConversation(st.ItemDir)
	application.Process = nil
	application.Clock = coreTestClock{now: now}
	return application, st, manifest
}

func coreManifest(t *testing.T, id, slug, state string, deep bool, now time.Time) model.Manifest {
	t.Helper()
	checkoutPath := "/workspace/must-remain-unchanged"
	manifest := model.NewManifest(id, slug, strings.ReplaceAll(slug, "-", " "), nil, deep, now,
		model.Repository{RootAtCreation: "/repository", GitCommonDir: "/repository/.git"},
		model.Checkout{Specification: model.CheckoutSpecification{Branch: model.ItemBranchName("item", id)}, ClaimGeneration: 1, Claim: &model.WorkspaceClaim{ID: "test-claim", Generation: 1, Executor: model.ExecutorLocal, Path: checkoutPath}},
	)
	manifest.State = state
	if state == model.StateClosed {
		manifest.Slug = ""
	}
	manifest.PrimaryConversation = &model.Conversation{Driver: "pi", ID: testutil.ID(t, 40), Locator: filepath.Join("sessions", "pi", "existing.jsonl")}
	return manifest
}
