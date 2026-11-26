package item

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
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

func (itemStore) Ensure() error                                          { return nil }
func (itemStore) SaveManifest(context.Context, model.Manifest) error     { return nil }
func (itemStore) AppendEvent(context.Context, string, model.Event) error { return nil }
func (s itemStore) ListManifests() ([]model.Manifest, []error)           { return s.items, s.errs }
func (itemStore) ItemDir(string) string                                  { return "" }

type creationGit struct {
	root              string
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

func noResolve(context.Context, contract.ResolveOptions) (model.Manifest, error) {
	return model.Manifest{}, nil
}

// Without a base, a new item starts from the local default branch, or from
// the current HEAD when that is unknown; an explicit base bypasses it.
func TestNewWorkItemResolvesItsBase(t *testing.T) {
	tests := []struct {
		name          string
		base          string
		git           creationGit
		wantCommit    string
		wantRevisions string
		wantWarning   string
	}{
		{"local default branch", "", creationGit{defaultBranch: "main", revisions: map[string]string{"": "current-head", "main": "main-head"}}, "main-head", ",main", ""},
		{"explicit base", "HEAD", creationGit{defaultBranch: "main", revisions: map[string]string{"HEAD": "current-head"}}, "current-head", "HEAD", ""},
		{"unknown default branch", "", creationGit{defaultBranchErr: errors.New("no default"), revisions: map[string]string{"": "current-head"}}, "current-head", "", "based new item on current HEAD"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			revisions := []string{}
			git := tc.git
			git.root, git.resolvedRevisions = t.TempDir(), &revisions
			service := New(itemStore{}, git, noResolve, testutil.Time, func() (string, error) { return testutil.ID(t, 3), nil }, nil, nil)
			created, err := service.NewWorkItem(context.Background(), NewOptions{Title: "Base", Base: tc.base, CWD: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			if created.Manifest.Repository.CreatedFromCommit != tc.wantCommit || strings.Join(revisions, ",") != tc.wantRevisions {
				t.Fatalf("created_from=%q revisions=%q", created.Manifest.Repository.CreatedFromCommit, revisions)
			}
			if tc.wantWarning == "" && len(created.Warnings) != 0 || tc.wantWarning != "" && (len(created.Warnings) != 1 || !strings.Contains(created.Warnings[0], tc.wantWarning)) {
				t.Fatalf("warnings = %+v", created.Warnings)
			}
		})
	}
}

// Default labels come from the user, the project and the environment, in
// that order, before the explicit ones; they can be skipped, and an invalid
// environment label is refused.
func TestNewWorkItemCombinesDefaultLabels(t *testing.T) {
	tests := []struct {
		name         string
		noDefaults   bool
		environment  string
		want         []string
		wantProject  bool
		wantWarnings []string
		wantError    string
	}{
		{name: "combined", environment: "Environment, shared", want: []string{"user", "shared", "project", "environment", "explicit"}, wantProject: true, wantWarnings: []string{"project warning"}},
		{name: "skipped", noDefaults: true, environment: "environment", want: []string{"explicit"}},
		{name: "invalid environment label", environment: "good,bad!label", wantProject: true, wantError: "WI_ITEM_DEFAULT_LABELS"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			projectCalled := false
			service := New(itemStore{}, creationGit{root: repo}, noResolve, testutil.Time, func() (string, error) { return testutil.ID(t, 6), nil }, []string{"User", "shared"}, func(root string) ([]string, []string, error) {
				projectCalled = true
				if root != repo {
					t.Fatalf("root = %q", root)
				}
				return []string{"Project", "shared"}, []string{"project warning"}, nil
			})
			created, err := service.NewWorkItem(context.Background(), NewOptions{Title: "Defaults", Labels: []string{"Explicit"}, NoDefaultLabels: tc.noDefaults, CWD: repo, Env: map[string]string{"WI_ITEM_DEFAULT_LABELS": tc.environment}})
			if projectCalled != tc.wantProject {
				t.Fatalf("project defaults read = %v", projectCalled)
			}
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(created.Manifest.Labels, tc.want) {
				t.Fatalf("labels = %+v", created.Manifest.Labels)
			}
			if !slices.Equal(created.Warnings, tc.wantWarnings) {
				t.Fatalf("warnings = %+v", created.Warnings)
			}
		})
	}
}

func TestNewPreparesAndShowReadsWorkItem(t *testing.T) {
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
	if created.Manifest.ID != id || created.Manifest.Slug != "focused-cleanup" || created.Manifest.State != model.StateBacklog || !created.Manifest.DeepWork || created.Description != "details\n" {
		t.Fatalf("prepared = %+v", created)
	}
	if created.Manifest.Checkout.Present() || created.Manifest.Checkout.Specification.Branch != model.ItemBranchName(created.Manifest.Slug, id) {
		t.Fatalf("checkout = %+v", created.Manifest.Checkout)
	}
	if _, err := os.Stat(created.ItemDir); !os.IsNotExist(err) {
		t.Fatalf("preparing wrote the item: %v", err)
	}
	if err := st.CreateItem(context.Background(), created.Manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(created.ItemDir, model.DescriptionFilename), []byte(created.Description), 0o600); err != nil {
		t.Fatal(err)
	}
	shown, err := service.Show(context.Background(), contract.ResolveOptions{Selector: id})
	if err != nil || shown.Description != "details\n" || shown.Manifest.ID != id {
		t.Fatalf("show = %+v, %v", shown, err)
	}
}
