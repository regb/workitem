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
	if err := st.AppendEvent(context.Background(), m.ID, model.NewEvent(now, "work_item.edited", "test", nil)); err != nil {
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
	if len(events) != 2 || events[0].Type != "work_item.created" || events[1].Type != "work_item.edited" {
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
	}, model.Checkout{Specification: model.CheckoutSpecification{Branch: model.ItemBranchName("item", id)}})
}

func TestResolveFromManifests(t *testing.T) {
	now := testutil.Time()
	fix := manifest(t, testutil.ID(t, 4), "Fix Race", now)
	cache := manifest(t, testutil.ID(t, 5), "Add Cache", now)
	cache.Labels = []string{"backend"}
	scheduler := manifest(t, testutil.ID(t, 7), "Draft Scheduler Cron Job Feature", now)
	oldScheduler := manifest(t, testutil.ID(t, 9), "Old Scheduler Cron Job", now)
	oldScheduler.State, oldScheduler.Slug = model.StateClosed, ""
	task := manifest(t, testutil.ID(t, 20), "TASK 1042 Cache Prototype", now)
	taskNotes := manifest(t, testutil.ID(t, 21), "TASK 1043 Cache Design Notes", now)
	missing := testutil.ID(t, 30)
	followUp := manifest(t, testutil.ID(t, 31), "Follow up "+missing, now)
	closedAuth := manifest(t, testutil.ID(t, 40), "Auth refactor", now)
	closedAuth.State, closedAuth.Slug, closedAuth.ClosedSlug = model.StateClosed, "", "auth-refactor"
	authV2 := manifest(t, testutil.ID(t, 41), "Auth refactor follow-up", now)
	authV2.Slug = "auth-refactor-v2"
	descriptions := map[string]string{
		scheduler.ID: "Related issue: ISSUE-42",
		taskNotes.ID: "Notes from task-1042",
		followUp.ID:  "Continues @@[Old](wi:" + missing + ")",
		authV2.ID:    "Continues auth-refactor",
	}
	describe := func(m model.Manifest) string { return descriptions[m.ID] }
	base := []model.Manifest{fix, cache, scheduler, oldScheduler, task, taskNotes, followUp, closedAuth, authV2}
	schedulerDocs := manifest(t, testutil.ID(t, 10), "Scheduler Docs", now)
	reusedAuth := manifest(t, testutil.ID(t, 42), "Auth refactor again", now)
	reusedAuth.Slug = "auth-refactor"
	closedAgain := closedAuth
	closedAgain.ID = testutil.ID(t, 43)
	for _, tc := range []struct {
		name     string
		selector string
		extra    []model.Manifest
		want     string // an item ID, or "ambiguous" or "not found"
	}{
		{"whole ID", fix.ID, nil, fix.ID},
		{"lower-case whole ID", strings.ToLower(followUp.ID), nil, followUp.ID},
		{"ID prefix", cache.ID[:12], nil, cache.ID},
		{"exact slug", fix.Slug, nil, fix.ID},
		{"active slug substring", "scheduler", nil, scheduler.ID},
		{"title words as slug", "cron job", nil, scheduler.ID},
		{"description mention", "ISSUE-42", nil, scheduler.ID},
		{"label", "backend", nil, cache.ID},
		{"ambiguous slug substring", "scheduler", []model.Manifest{schedulerDocs}, "ambiguous"},
		// A unique slug substring wins over another item's description mention.
		{"slug substring before description", "task-1042", nil, task.ID},
		// A whole ID names its item or nothing, never an item mentioning it.
		{"missing whole ID", missing, nil, "not found"},
		{"missing lower-case whole ID", strings.ToLower(missing), nil, "not found"},
		// A closed item keeps the slug it had, before slugs containing it or mentions.
		{"closed item's slug", "auth-refactor", nil, closedAuth.ID},
		{"active slug before closed slug", "auth-refactor", []model.Manifest{reusedAuth}, reusedAuth.ID},
		{"two closed items with the slug", "auth-refactor", []model.Manifest{closedAgain}, "ambiguous"},
		{"empty", " ", nil, "not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.ResolveFromManifests(append(append([]model.Manifest{}, base...), tc.extra...), tc.selector, describe)
			var ambiguous store.AmbiguousError
			switch tc.want {
			case "ambiguous":
				if !errors.As(err, &ambiguous) {
					t.Fatalf("got %+v err=%v, want ambiguous", got.ID, err)
				}
			case "not found":
				if !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("got %+v err=%v, want not found", got.ID, err)
				}
			default:
				if err != nil || got.ID != tc.want {
					t.Fatalf("got %s err=%v, want %s", got.ID, err, tc.want)
				}
			}
		})
	}
}

func TestResolveActiveSlugFromManifestsAcceptsExactOrUniqueActiveSubstring(t *testing.T) {
	now := testutil.Time()
	active := manifest(t, testutil.ID(t, 17), "Exact Active", now)
	similar := manifest(t, testutil.ID(t, 19), "Exact Action", now)
	archived := manifest(t, testutil.ID(t, 18), "Old", now)
	archived.State, archived.Slug = model.StateClosed, ""
	items := []model.Manifest{active, similar, archived}
	for _, selector := range []string{active.Slug, "tive", "TIVE"} {
		resolved, err := store.ResolveActiveSlugFromManifests(items, selector)
		if err != nil || resolved.ID != active.ID {
			t.Fatalf("ResolveActiveSlug(%q) = %+v err=%v", selector, resolved, err)
		}
	}
	var ambiguous store.AmbiguousError
	if _, err := store.ResolveActiveSlugFromManifests(items, "act"); !errors.As(err, &ambiguous) || len(ambiguous.Candidates) != 2 {
		t.Fatalf("ambiguous error = %T %v", err, err)
	}
	for _, selector := range []string{active.ID, "Exact Active", "old", ""} {
		if _, err := store.ResolveActiveSlugFromManifests(items, selector); err == nil {
			t.Fatalf("ResolveActiveSlug(%q) unexpectedly succeeded", selector)
		}
	}
}

// The store resolves over the manifests on disk, reading descriptions from
// the item directory, and reports a malformed manifest rather than skipping it.
func TestStoreResolvesOnDiskAndReportsMalformedManifests(t *testing.T) {
	st := store.New(t.TempDir())
	now := testutil.Time()
	described := manifest(t, testutil.ID(t, 50), "Described", now)
	other := manifest(t, testutil.ID(t, 51), "Other", now)
	for _, m := range []model.Manifest{described, other} {
		if err := st.CreateItem(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(st.ItemDir(described.ID), model.DescriptionFilename), []byte("Related issue: ISSUE-42"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := st.Resolve("ISSUE-42"); err != nil || got.ID != described.ID {
		t.Fatalf("Resolve = %s err=%v", got.ID, err)
	}
	if got, err := st.ResolveActiveSlug("describ"); err != nil || got.ID != described.ID {
		t.Fatalf("ResolveActiveSlug = %s err=%v", got.ID, err)
	}
	for name, content := range map[string]string{
		"parse":   "{not json",
		"invalid": `{"id":"` + other.ID + `","title":" "}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(st.ManifestPath(other.ID), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := st.LoadManifest(other.ID); err == nil || !strings.HasPrefix(err.Error(), name+" manifest") {
				t.Fatalf("LoadManifest err=%v", err)
			}
			items, errs := st.ListManifests()
			if len(errs) != 1 || len(items) != 1 || items[0].ID != described.ID {
				t.Fatalf("ListManifests = %d items, errs=%v", len(items), errs)
			}
			if _, err := st.Resolve(described.ID); err == nil {
				t.Fatal("Resolve ignored a malformed manifest")
			}
		})
	}
}
