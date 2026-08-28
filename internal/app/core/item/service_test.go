package item

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

type itemStore struct {
	items []model.Manifest
	errs  []error
}

func (itemStore) Ensure() error                                                    { return nil }
func (itemStore) CreateItem(context.Context, model.Manifest, ...model.Event) error { return nil }
func (itemStore) RemoveItem(string) error                                          { return nil }
func (itemStore) SaveManifest(context.Context, model.Manifest) error               { return nil }
func (itemStore) AppendEvent(context.Context, string, model.Event) error           { return nil }
func (itemStore) ReadEvents(string) ([]model.Event, error)                         { return nil, nil }
func (s itemStore) ListManifests() ([]model.Manifest, []error)                     { return s.items, s.errs }
func (itemStore) ItemDir(string) string                                            { return "" }
func TestUniqueSlugUsesOnlyActiveAliases(t *testing.T) {
	s := &Service{store: itemStore{items: []model.Manifest{{Slug: "topic", State: model.StateWorking}, {Slug: "topic-2", State: model.StateArchived}}}}
	got, err := s.UniqueSlug("topic")
	if err != nil || got != "topic-2" {
		t.Fatalf("slug=%q err=%v", got, err)
	}
}
func TestExplicitSlugRejectsActiveCollision(t *testing.T) {
	s := &Service{store: itemStore{items: []model.Manifest{{Slug: "topic", State: model.StateBacklog}}}}
	if _, err := s.chooseSlug("topic", "ignored"); err == nil {
		t.Fatal("expected active slug collision")
	}
}

type creationGit struct {
	root              string
	home              model.RepositoryHomeInfo
	branchExists      bool
	defaultBranch     string
	defaultBranchErr  error
	revisions         map[string]string
	resolvedRevisions *[]string
}

func (g creationGit) DetectRepository(_ context.Context, _ string, revision string) (model.RepositoryInfo, error) {
	if g.resolvedRevisions != nil {
		*g.resolvedRevisions = append(*g.resolvedRevisions, revision)
	}
	commit := "abc"
	if value := g.revisions[revision]; value != "" {
		commit = value
	}
	return model.RepositoryInfo{Repository: model.Repository{RootAtCreation: g.root, GitCommonDir: filepath.Join(g.root, ".git"), CreatedFromCommit: commit}, Commit: commit}, nil
}
func (g creationGit) RepositoryHome(context.Context, string) (model.RepositoryHomeInfo, error) {
	if g.home.Path != "" || g.home.Bare || g.home.Detached {
		return g.home, nil
	}
	return model.RepositoryHomeInfo{Path: g.root, Branch: g.defaultBranch}, nil
}
func (g creationGit) DefaultBranch(context.Context, string) (string, error) {
	if g.defaultBranchErr != nil {
		return "", g.defaultBranchErr
	}
	if g.defaultBranch == "" {
		return "main", nil
	}
	return g.defaultBranch, nil
}
func (g creationGit) BranchExists(context.Context, string, string) (bool, error) {
	return g.branchExists, nil
}

func TestNewWorkItemUsesLocalDefaultBranchWhenBaseOmitted(t *testing.T) {
	st := store.New(t.TempDir())
	revisions := []string{}
	git := creationGit{root: t.TempDir(), defaultBranch: "main", revisions: map[string]string{"": "current-head", "main": "main-head"}, resolvedRevisions: &revisions}
	service := New(st, git, func(context.Context, contract.ResolveOptions) (model.Manifest, error) { return model.Manifest{}, nil }, testutil.Time, func() (string, error) { return testutil.ID(t, 3), nil }, nil, nil)
	created, err := service.NewWorkItem(context.Background(), NewOptions{Title: "Default base", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if created.Manifest.Repository.CreatedFromCommit != "main-head" || strings.Join(revisions, ",") != ",main" {
		t.Fatalf("created_from=%q revisions=%q", created.Manifest.Repository.CreatedFromCommit, revisions)
	}
}

func TestNewWorkItemExplicitBaseBypassesDefaultBranch(t *testing.T) {
	st := store.New(t.TempDir())
	revisions := []string{}
	git := creationGit{root: t.TempDir(), defaultBranch: "main", revisions: map[string]string{"HEAD": "current-head"}, resolvedRevisions: &revisions}
	service := New(st, git, func(context.Context, contract.ResolveOptions) (model.Manifest, error) { return model.Manifest{}, nil }, testutil.Time, func() (string, error) { return testutil.ID(t, 2), nil }, nil, nil)
	created, err := service.NewWorkItem(context.Background(), NewOptions{Title: "Explicit base", Base: "HEAD", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if created.Manifest.Repository.CreatedFromCommit != "current-head" || strings.Join(revisions, ",") != "HEAD" {
		t.Fatalf("created_from=%q revisions=%q", created.Manifest.Repository.CreatedFromCommit, revisions)
	}
}

func TestNewWorkItemFallsBackToCurrentHeadWhenDefaultUnknown(t *testing.T) {
	st := store.New(t.TempDir())
	git := creationGit{root: t.TempDir(), defaultBranchErr: errors.New("no default"), revisions: map[string]string{"": "current-head"}}
	service := New(st, git, func(context.Context, contract.ResolveOptions) (model.Manifest, error) { return model.Manifest{}, nil }, testutil.Time, func() (string, error) { return testutil.ID(t, 1), nil }, nil, nil)
	created, err := service.NewWorkItem(context.Background(), NewOptions{Title: "Fallback", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if created.Manifest.Repository.CreatedFromCommit != "current-head" || len(created.Warnings) != 1 || !strings.Contains(created.Warnings[0], "based new item on current HEAD") {
		t.Fatalf("created=%+v", created)
	}
}

func TestNewHomeWorkItemBindsPrimaryDefaultCheckout(t *testing.T) {
	st := store.New(t.TempDir())
	root := t.TempDir()
	git := creationGit{root: root, defaultBranch: "main", revisions: map[string]string{"main": "main-head"}}
	service := New(st, git, func(context.Context, contract.ResolveOptions) (model.Manifest, error) { return model.Manifest{}, nil }, testutil.Time, func() (string, error) { return testutil.ID(t, 11), nil }, nil, nil)
	created, err := service.NewWorkItem(context.Background(), NewOptions{Title: "Mainline", Home: true, CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	checkout := created.Manifest.Checkout
	if checkout.Kind != model.WorkspaceKindRepositoryHome || !checkout.Present() || checkout.Path == nil || *checkout.Path != root || checkout.Branch != "main" || created.Manifest.Repository.CreatedFromCommit != "main-head" {
		t.Fatalf("checkout=%+v", checkout)
	}
}

func TestNewHomeWorkItemRequiresPrimaryDefaultCheckout(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name string
		home model.RepositoryHomeInfo
		want string
	}{
		{"bare", model.RepositoryHomeInfo{Bare: true}, "no primary working checkout"},
		{"detached", model.RepositoryHomeInfo{Path: root, Detached: true}, "is detached"},
		{"wrong branch", model.RepositoryHomeInfo{Path: root, Branch: "feature"}, "expected local default branch main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := store.New(t.TempDir())
			git := creationGit{root: "/repo", home: tc.home, defaultBranch: "main", revisions: map[string]string{"main": "main-head"}}
			service := New(st, git, func(context.Context, contract.ResolveOptions) (model.Manifest, error) { return model.Manifest{}, nil }, testutil.Time, func() (string, error) { return testutil.ID(t, 12), nil }, nil, nil)
			_, err := service.NewWorkItem(context.Background(), NewOptions{Title: "Mainline", Home: true, CWD: t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestNewHomeWorkItemRejectsExistingClaim(t *testing.T) {
	st := store.New(t.TempDir())
	root := t.TempDir()
	now := testutil.Time()
	path := root
	existing := model.NewManifest(testutil.ID(t, 13), "existing", "Existing", nil, false, now, model.Repository{RootAtCreation: root, GitCommonDir: filepath.Join(root, ".git")}, model.Checkout{Kind: model.WorkspaceKindRepositoryHome, Path: &path, Branch: "main"})
	if err := st.CreateItem(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	git := creationGit{root: root, defaultBranch: "main", revisions: map[string]string{"main": "main-head"}}
	service := New(st, git, func(context.Context, contract.ResolveOptions) (model.Manifest, error) { return model.Manifest{}, nil }, testutil.Time, func() (string, error) { return testutil.ID(t, 14), nil }, nil, nil)
	if _, err := service.NewWorkItem(context.Background(), NewOptions{Title: "Second", Home: true, CWD: root}); err == nil || !strings.Contains(err.Error(), "already claimed") {
		t.Fatalf("err=%v", err)
	}
}

func TestNewWorkItemCombinesDefaultLabels(t *testing.T) {
	st := store.New(t.TempDir())
	repo := t.TempDir()
	service := New(st, creationGit{root: repo}, func(context.Context, contract.ResolveOptions) (model.Manifest, error) {
		return model.Manifest{}, nil
	}, testutil.Time, func() (string, error) { return testutil.ID(t, 6), nil }, []string{"User", "shared"}, func(root string) ([]string, []string, error) {
		if root != repo {
			t.Fatalf("root = %q", root)
		}
		return []string{"Project", "shared"}, []string{"project warning"}, nil
	})
	created, err := service.NewWorkItem(context.Background(), NewOptions{Title: "Defaults", Labels: []string{"Explicit", "environment"}, CWD: repo, Env: map[string]string{"WI_ITEM_DEFAULT_LABELS": "Environment, shared"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"user", "shared", "project", "environment", "explicit"}
	if got := created.Manifest.Labels; len(got) != len(want) || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("labels = %+v", got)
	}
	if len(created.Warnings) != 1 || created.Warnings[0] != "project warning" {
		t.Fatalf("warnings = %+v", created.Warnings)
	}
}

func TestNewWorkItemCanSkipDefaultLabels(t *testing.T) {
	st := store.New(t.TempDir())
	repo := t.TempDir()
	projectCalled := false
	service := New(st, creationGit{root: repo}, func(context.Context, contract.ResolveOptions) (model.Manifest, error) {
		return model.Manifest{}, nil
	}, testutil.Time, func() (string, error) { return testutil.ID(t, 5), nil }, []string{"user"}, func(string) ([]string, []string, error) {
		projectCalled = true
		return []string{"project"}, nil, nil
	})
	created, err := service.NewWorkItem(context.Background(), NewOptions{Title: "No defaults", Labels: []string{"explicit"}, NoDefaultLabels: true, CWD: repo, Env: map[string]string{"WI_ITEM_DEFAULT_LABELS": "environment"}})
	if err != nil {
		t.Fatal(err)
	}
	if projectCalled || len(created.Manifest.Labels) != 1 || created.Manifest.Labels[0] != "explicit" {
		t.Fatalf("called=%v labels=%+v", projectCalled, created.Manifest.Labels)
	}
}

func TestNewWorkItemRejectsInvalidEnvironmentDefaultLabel(t *testing.T) {
	st := store.New(t.TempDir())
	service := New(st, creationGit{root: t.TempDir()}, func(context.Context, contract.ResolveOptions) (model.Manifest, error) {
		return model.Manifest{}, nil
	}, testutil.Time, func() (string, error) { return testutil.ID(t, 4), nil }, nil, nil)
	_, err := service.NewWorkItem(context.Background(), NewOptions{Title: "Invalid environment", CWD: t.TempDir(), Env: map[string]string{"WI_ITEM_DEFAULT_LABELS": "good,bad!label"}})
	if err == nil || !strings.Contains(err.Error(), "WI_ITEM_DEFAULT_LABELS") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewAndShowWorkItem(t *testing.T) {
	st := store.New(t.TempDir())
	now := testutil.Time()
	id := testutil.ID(t, 7)
	service := New(st, creationGit{root: t.TempDir()}, func(_ context.Context, opts contract.ResolveOptions) (model.Manifest, error) {
		return st.Resolve(opts.Selector)
	}, func() time.Time { return now }, func() (string, error) { return id, nil }, nil, nil)
	created, err := service.NewWorkItem(context.Background(), NewOptions{Title: "  Focused cleanup  ", Description: "details\n", Labels: []string{"Refactor"}, DeepWork: true, CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if created.Manifest.ID != id || created.Manifest.Slug != "focused-cleanup" || created.Manifest.State != model.StateBacklog || !created.Manifest.DeepWork {
		t.Fatalf("manifest = %+v", created.Manifest)
	}
	if created.Manifest.Checkout.Present() || created.Manifest.Checkout.Branch != model.ItemBranchName(created.Manifest.Slug, id) {
		t.Fatalf("checkout = %+v", created.Manifest.Checkout)
	}
	if info, err := os.Stat(filepath.Join(created.ItemDir, model.DescriptionFilename)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("description stat = %v, %v", info, err)
	}
	shown, err := service.Show(context.Background(), contract.ResolveOptions{Selector: id})
	if err != nil || shown.Description != "details\n" || shown.Manifest.ID != id {
		t.Fatalf("show = %+v, %v", shown, err)
	}
}
