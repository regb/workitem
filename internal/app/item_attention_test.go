package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

type coreTestClock struct{ now time.Time }

func (c coreTestClock) Now() time.Time { return c.now }

func TestSetWorkItemStateTransitionsWithoutWorkspaceAdapters(t *testing.T) {
	tests := []struct {
		name  string
		from  string
		to    string
		valid bool
	}{
		{"backlog to working", model.StateBacklog, model.StateWorking, true},
		{"working to waiting", model.StateWorking, model.StateWaiting, true},
		{"waiting to working", model.StateWaiting, model.StateWorking, true},
		{"working to backlog", model.StateWorking, model.StateBacklog, true},
		{"waiting to backlog", model.StateWaiting, model.StateBacklog, true},
		{"backlog to archived", model.StateBacklog, model.StateArchived, true},
		{"working to archived", model.StateWorking, model.StateArchived, true},
		{"archived to backlog", model.StateArchived, model.StateBacklog, true},
		{"backlog to waiting", model.StateBacklog, model.StateWaiting, false},
		{"archived to working", model.StateArchived, model.StateWorking, false},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now := testutil.Time().Add(time.Duration(i) * time.Minute)
			application, st, manifest := newCoreTestApp(t, now, tc.from, false)
			originalCheckout := manifest.Checkout
			originalPi := manifest.RootPiSession

			result, err := application.SetWorkItemState(context.Background(), ResolveOptions{Selector: manifest.ID}, tc.to, false)
			if !tc.valid {
				if err == nil {
					t.Fatalf("expected %s -> %s to fail", tc.from, tc.to)
				}
				persisted, loadErr := st.LoadManifest(manifest.ID)
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				if persisted.State != tc.from {
					t.Fatalf("failed transition persisted state %q", persisted.State)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !result.Changed || result.PreviousState != tc.from || result.State != tc.to {
				t.Fatalf("transition result = %+v", result)
			}
			persisted, err := st.LoadManifest(manifest.ID)
			if err != nil {
				t.Fatal(err)
			}
			if persisted.State != tc.to || !persisted.StateChangedAt.Equal(now) {
				t.Fatalf("persisted state = %+v", persisted)
			}
			shown, err := application.WorkItemState(context.Background(), ResolveOptions{Selector: manifest.ID})
			if err != nil || shown.State != tc.to || shown.WorkItemID != manifest.ID {
				t.Fatalf("state read = %+v err=%v", shown, err)
			}
			if !reflect.DeepEqual(persisted.Checkout, originalCheckout) || !reflect.DeepEqual(persisted.RootPiSession, originalPi) {
				t.Fatalf("state transition changed workspace fields: before=%+v/%+v after=%+v/%+v", originalCheckout, originalPi, persisted.Checkout, persisted.RootPiSession)
			}
			if tc.to == model.StateArchived && persisted.Slug != "" {
				t.Fatalf("archived slug = %q", persisted.Slug)
			}
			if tc.from == model.StateArchived && tc.to == model.StateBacklog && persisted.Slug == "" {
				t.Fatal("restored backlog item did not receive a slug")
			}
			events, err := st.ReadEvents(manifest.ID)
			if err != nil {
				t.Fatal(err)
			}
			last := events[len(events)-1]
			if last.Type != "work_item.state_set" || last.Data["workspace_unchanged"] != true {
				t.Fatalf("state event = %+v", last)
			}
		})
	}
}

func TestSetWorkItemStateEnforcesDeepWorkCapacity(t *testing.T) {
	now := testutil.Time()
	application, st, target := newCoreTestApp(t, now, model.StateBacklog, true)
	application.DeepWorkConfig = config.DeepWorkConfig{MaxActive: 1}
	active := coreManifest(t, testutil.ID(t, 31), "active-deep", model.StateWorking, true, now)
	if err := st.CreateItem(context.Background(), active); err != nil {
		t.Fatal(err)
	}

	if _, err := application.SetWorkItemState(context.Background(), ResolveOptions{Selector: target.ID}, model.StateWorking, false); err == nil || !strings.Contains(err.Error(), "Deep work active limit reached") {
		t.Fatalf("capacity error = %v", err)
	}
	persisted, err := st.LoadManifest(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != model.StateBacklog {
		t.Fatalf("failed capacity transition persisted %q", persisted.State)
	}

	result, err := application.SetWorkItemState(context.Background(), ResolveOptions{Selector: target.ID}, model.StateWorking, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != model.StateWorking || !result.Changed {
		t.Fatalf("forced transition = %+v", result)
	}
	events, err := st.ReadEvents(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundOverride := false
	for _, event := range events {
		foundOverride = foundOverride || event.Type == "deep_work_limit.overridden"
	}
	if !foundOverride {
		t.Fatalf("events do not contain capacity override: %+v", events)
	}
}

func TestAttentionFactsUseOnlyDurableFiles(t *testing.T) {
	base := testutil.Time()
	application, st, manifest := newCoreTestApp(t, base.Add(3*time.Minute), model.StateWorking, false)
	manifest.RootPiSession = &model.PiSession{ID: testutil.ID(t, 41), Path: filepath.Join("sessions", "pi", "root.jsonl")}
	if err := st.SaveManifest(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	piPath := filepath.Join(st.ItemDir(manifest.ID), filepath.FromSlash(manifest.RootPiSession.Path))
	if err := os.MkdirAll(filepath.Dir(piPath), 0o700); err != nil {
		t.Fatal(err)
	}
	appendCorePiMessage(t, piPath, base.Add(time.Minute), "user", "please inspect")
	appendCorePiMessage(t, piPath, base.Add(2*time.Minute), "assistant", "done")

	deferred, err := application.RecordAttentionDefer(context.Background(), ResolveOptions{Selector: manifest.ID})
	if err != nil {
		t.Fatal(err)
	}
	if deferred.Activity.LastDeferredAt == nil || !deferred.DeferredAt.Equal(base.Add(3*time.Minute)) {
		t.Fatalf("defer result = %+v", deferred)
	}
	activity, err := application.WorkItemActivity(context.Background(), ResolveOptions{Selector: manifest.ID})
	if err != nil {
		t.Fatal(err)
	}
	if activity.Activity.LastRequestedAt == nil || !activity.Activity.LastRequestedAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("requested activity = %+v", activity.Activity)
	}
	if activity.Activity.LastCompletedAt == nil || !activity.Activity.LastCompletedAt.Equal(base.Add(2*time.Minute)) {
		t.Fatalf("completed activity = %+v", activity.Activity)
	}
	if activity.Activity.LastDeferredAt == nil || !activity.Activity.LastDeferredAt.Equal(base.Add(3*time.Minute)) {
		t.Fatalf("deferred activity = %+v", activity.Activity)
	}
	for _, warning := range append(activity.Warnings, deferred.Warnings...) {
		if strings.Contains(strings.ToLower(warning), "tmux") || strings.Contains(strings.ToLower(warning), "process") {
			t.Fatalf("durable attention inspection touched runtime adapters: %q", warning)
		}
	}

	persisted, err := st.LoadManifest(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != model.StateWorking || !reflect.DeepEqual(persisted.Checkout, manifest.Checkout) {
		t.Fatalf("attention mutation changed item/workspace state: %+v", persisted)
	}
}

func TestRecordAttentionDeferRejectsNonWorkingState(t *testing.T) {
	application, st, manifest := newCoreTestApp(t, testutil.Time(), model.StateBacklog, false)
	if _, err := application.RecordAttentionDefer(context.Background(), ResolveOptions{Selector: manifest.ID}); err == nil {
		t.Fatal("expected backlog defer to fail")
	}
	events, err := st.ReadEvents(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == attentionDeferredEvent {
			t.Fatalf("failed defer appended event: %+v", event)
		}
	}
}

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
	application.Process = nil
	application.Clock = coreTestClock{now: now}
	return application, st, manifest
}

func coreManifest(t *testing.T, id, slug, state string, deep bool, now time.Time) model.Manifest {
	t.Helper()
	checkoutPath := "/workspace/must-remain-unchanged"
	manifest := model.NewManifest(id, slug, strings.ReplaceAll(slug, "-", " "), nil, deep, now,
		model.Repository{RootAtCreation: "/repository", GitCommonDir: "/repository/.git"},
		model.Checkout{Path: &checkoutPath, Branch: model.ItemBranchName("item", id)},
	)
	manifest.State = state
	if state == model.StateArchived {
		manifest.Slug = ""
	}
	manifest.RootPiSession = &model.PiSession{ID: testutil.ID(t, 40), Path: filepath.Join("sessions", "pi", "existing.jsonl")}
	return manifest
}

func appendCorePiMessage(t *testing.T, path string, timestamp time.Time, role, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	entry := map[string]any{
		"type": "message", "timestamp": timestamp.Format(time.RFC3339Nano),
		"message": map[string]any{"role": role, "content": []map[string]any{{"type": "text", "text": text}}},
	}
	if err := json.NewEncoder(f).Encode(entry); err != nil {
		t.Fatal(err)
	}
}
