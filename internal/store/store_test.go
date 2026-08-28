package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

func TestItemMutationLockSurvivesDeletionAndStaleSaveCannotRecreateItem(t *testing.T) {
	st := store.New(t.TempDir())
	item := manifest(t, testutil.ID(t, 78), "Delete lock", testutil.Time())
	if err := st.CreateItem(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(st.LockPath(item.ID), st.ItemDir(item.ID)+string(filepath.Separator)) {
		t.Fatalf("item lock %s is inside deletable item directory", st.LockPath(item.ID))
	}
	if err := st.RemoveItem(item.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveManifest(context.Background(), item); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stale save error = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(st.ItemDir(item.ID)); !os.IsNotExist(err) {
		t.Fatalf("stale save recreated item directory: %v", err)
	}
}

func TestCreateSanitizesCredentialBearingRemoteURL(t *testing.T) {
	st := store.New(t.TempDir())
	item := manifest(t, testutil.ID(t, 79), "Sanitize remote", testutil.Time())
	item.Repository.RemoteURL = "https://user:secret@example.com/owner/repository.git?access_token=secret"
	if err := st.CreateItem(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(st.ManifestPath(item.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "secret") || strings.Contains(string(contents), "user@") {
		t.Fatalf("credential persisted in manifest: %s", contents)
	}
	loaded, err := st.LoadManifest(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Repository.RemoteURL != "https://example.com/owner/repository.git" {
		t.Fatalf("remote URL = %q", loaded.Repository.RemoteURL)
	}
}

func TestCreateLoadListAndEvents(t *testing.T) {
	st := store.New(t.TempDir())
	now := testutil.Time()
	m := manifest(t, testutil.ID(t, 1), "Fix refresh-token race", now)
	ev := model.NewEvent(now, "work_item.created", "user", map[string]any{"slug": m.Slug})
	if err := st.CreateItem(context.Background(), m, ev); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.LoadManifest(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != m.ID || loaded.Title != m.Title {
		t.Fatalf("loaded manifest mismatch: %+v", loaded)
	}
	if _, err := os.Stat(filepath.Join(st.ItemDir(m.ID), "sessions", "pi")); err != nil {
		t.Fatalf("sessions/pi not created: %v", err)
	}
	items, errs := st.ListManifests()
	if len(errs) != 0 {
		t.Fatalf("list warnings: %v", errs)
	}
	if len(items) != 1 || items[0].ID != m.ID {
		t.Fatalf("items = %+v", items)
	}
	events, err := st.ReadEvents(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "work_item.created" {
		t.Fatalf("events = %+v", events)
	}
}

func TestSaveManifestAtomic(t *testing.T) {
	st := store.New(t.TempDir())
	m := manifest(t, testutil.ID(t, 2), "Original", testutil.Time())
	if err := st.CreateItem(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	m.Title = "Updated"
	m.UpdatedAt = m.UpdatedAt.Add(time.Minute)
	if err := st.SaveManifest(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.LoadManifest(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Title != "Updated" {
		t.Fatalf("Title = %q", loaded.Title)
	}
	entries, err := os.ReadDir(st.ItemDir(m.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".manifest-") {
			t.Fatalf("temporary manifest file left behind: %s", entry.Name())
		}
	}
}

func TestTerminalRuntimeRoundTripAndRemove(t *testing.T) {
	st := store.New(t.TempDir())
	now := testutil.Time()
	m := manifest(t, testutil.ID(t, 34), "Workspace Handles", now)
	if err := st.CreateItem(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	workspace := model.TerminalRuntime{TmuxWindow: "agent", TmuxPaneID: "%1", TmuxPanePID: 123, UpdatedAt: now}
	if err := st.SaveTerminalRuntime(context.Background(), m.ID, workspace); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.LoadTerminalRuntime(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.TmuxPanePID != 123 || loaded.TmuxPaneID != "%1" {
		t.Fatalf("loaded = %+v", loaded)
	}
	if err := st.RemoveTerminalRuntime(context.Background(), m.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err = st.LoadTerminalRuntime(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != nil {
		t.Fatalf("workspace handle cache should be gone: %+v", loaded)
	}
}

func TestAppendEvent(t *testing.T) {
	st := store.New(t.TempDir())
	now := testutil.Time()
	m := manifest(t, testutil.ID(t, 3), "Events", now)
	if err := st.CreateItem(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendEvent(context.Background(), m.ID, model.NewEvent(now, "one", "test", nil)); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendEvent(context.Background(), m.ID, model.NewEvent(now, "two", "test", nil)); err != nil {
		t.Fatal(err)
	}
	events, err := st.ReadEvents(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{events[0].Type, events[1].Type}; got[0] != "one" || got[1] != "two" {
		t.Fatalf("events order = %+v", got)
	}
}

func TestResolveByIDPrefixAndSlug(t *testing.T) {
	st := store.New(t.TempDir())
	m1 := manifest(t, testutil.ID(t, 4), "Fix Race", testutil.Time())
	m2 := manifest(t, testutil.ID(t, 5), "Add Cache", testutil.Time().Add(time.Minute))
	for _, m := range []model.Manifest{m1, m2} {
		if err := st.CreateItem(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	byID, err := st.Resolve(m1.ID)
	if err != nil || byID.ID != m1.ID {
		t.Fatalf("resolve by id = %+v, %v", byID, err)
	}
	byPrefix, err := st.Resolve(m2.ID[:12])
	if err != nil || byPrefix.ID != m2.ID {
		t.Fatalf("resolve by prefix = %+v, %v", byPrefix, err)
	}
	bySlug, err := st.Resolve(m1.Slug)
	if err != nil || bySlug.ID != m1.ID {
		t.Fatalf("resolve by slug = %+v, %v", bySlug, err)
	}
}

func TestResolveByUniquePartialActiveSelector(t *testing.T) {
	st := store.New(t.TempDir())
	now := testutil.Time()
	m1 := manifest(t, testutil.ID(t, 7), "Draft Scheduler Cron Job Feature", now)
	m2 := manifest(t, testutil.ID(t, 8), "Add Cache", now.Add(time.Minute))
	m2.Labels = []string{"backend"}
	m3 := manifest(t, testutil.ID(t, 9), "Old Scheduler Cron Job", now.Add(2*time.Minute))
	m3.State = model.StateArchived
	m3.Slug = ""
	for _, m := range []model.Manifest{m1, m2, m3} {
		if err := st.CreateItem(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(st.ItemDir(m1.ID), model.DescriptionFilename), []byte("Related issue: ISSUE-42"), 0o600); err != nil {
		t.Fatal(err)
	}
	for selector, want := range map[string]string{
		"scheduler": m1.ID,
		"cron job":  m1.ID,
		"ISSUE-42":  m1.ID,
		"backend":   m2.ID,
	} {
		got, err := st.Resolve(selector)
		if err != nil {
			t.Fatalf("resolve %q: %v", selector, err)
		}
		if got.ID != want {
			t.Fatalf("resolve %q = %s, want %s", selector, got.ID, want)
		}
	}

	m4 := manifest(t, testutil.ID(t, 10), "Scheduler Docs", now.Add(3*time.Minute))
	if err := st.CreateItem(context.Background(), m4); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Resolve("scheduler"); err == nil {
		t.Fatal("expected ambiguous partial selector")
	} else {
		var ambiguous store.AmbiguousError
		if !errors.As(err, &ambiguous) {
			t.Fatalf("expected AmbiguousError, got %T %v", err, err)
		}
	}
}

func TestResolvePrefersUniqueSlugSubstringOverDescriptionReference(t *testing.T) {
	st := store.New(t.TempDir())
	now := testutil.Time()
	target := manifest(t, testutil.ID(t, 20), "TASK 1042 Cache Prototype", now)
	reference := manifest(t, testutil.ID(t, 21), "TASK 1043 Cache Design Notes", now.Add(time.Minute))
	for _, item := range []model.Manifest{target, reference} {
		if err := st.CreateItem(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(st.ItemDir(reference.ID), model.DescriptionFilename), []byte("Notes from task-1042"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := st.Resolve("task-1042")
	if err != nil || resolved.ID != target.ID {
		t.Fatalf("slug-priority resolution = %+v err=%v", resolved, err)
	}
}

func TestResolveActiveSlugAcceptsExactOrUniqueActiveSubstring(t *testing.T) {
	st := store.New(t.TempDir())
	now := testutil.Time()
	active := manifest(t, testutil.ID(t, 17), "Exact Active", now)
	similar := manifest(t, testutil.ID(t, 19), "Exact Action", now)
	archived := manifest(t, testutil.ID(t, 18), "Old", now)
	archived.State = model.StateArchived
	archived.Slug = ""
	for _, item := range []model.Manifest{active, similar, archived} {
		if err := st.CreateItem(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}
	for _, selector := range []string{active.Slug, "tive", "TIVE"} {
		resolved, err := st.ResolveActiveSlug(selector)
		if err != nil || resolved.ID != active.ID {
			t.Fatalf("ResolveActiveSlug(%q) = %+v err=%v", selector, resolved, err)
		}
	}
	if _, err := st.ResolveActiveSlug("act"); err == nil {
		t.Fatal("expected ambiguous active slug substring")
	} else {
		var ambiguous store.AmbiguousError
		if !errors.As(err, &ambiguous) || len(ambiguous.Candidates) != 2 {
			t.Fatalf("ambiguous error = %T %v", err, err)
		}
	}
	for _, selector := range []string{active.ID, "Exact Active", "old", ""} {
		if _, err := st.ResolveActiveSlug(selector); err == nil {
			t.Fatalf("ResolveActiveSlug(%q) unexpectedly succeeded", selector)
		}
	}
}

func TestCreateItemRollsBackOnEventFailure(t *testing.T) {
	st := store.New(t.TempDir())
	m := manifest(t, testutil.ID(t, 6), "Rollback", testutil.Time())
	bad := model.NewEvent(testutil.Time(), "bad", "test", map[string]any{"unsupported": func() {}})
	if err := st.CreateItem(context.Background(), m, bad); err == nil {
		t.Fatal("expected CreateItem to fail")
	}
	if _, err := os.Stat(st.ItemDir(m.ID)); !os.IsNotExist(err) {
		t.Fatalf("item directory was not rolled back, stat err=%v", err)
	}
}

func manifest(t *testing.T, id, title string, now time.Time) model.Manifest {
	t.Helper()
	slug := model.Slugify(title)
	return model.NewManifest(id, slug, title, nil, false, now, model.Repository{
		RootAtCreation:    t.TempDir(),
		GitCommonDir:      filepath.Join(t.TempDir(), ".git"),
		RemoteURL:         "",
		CreatedFromCommit: strings.Repeat("a", 40),
	}, model.Checkout{
		Path:   nil,
		Branch: model.ItemBranchName("item", id),
	})
}
