package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

type baseInspector struct {
	calls                  int
	path, revision, common string
	err                    error
}

func (g *baseInspector) DetectRepository(_ context.Context, path, revision string) (model.RepositoryInfo, error) {
	g.calls++
	g.path = path
	g.revision = revision
	return model.RepositoryInfo{Commit: "captured-commit", Repository: model.Repository{GitCommonDir: g.common}}, g.err
}

func TestRepositoryBaseUsesOwnedStoreForNamedRevisions(t *testing.T) {
	for _, base := range []string{"", "^", "main", "main~1", "refs/tags/v1", "deadbeef"} {
		got, err := resolveRepositoryBase(context.Background(), base, "", nil, coordinator.RepositorySourceResult{}, nil)
		want := base
		if base == "^" {
			want = ""
		}
		if err != nil || got != want {
			t.Errorf("%q -> %q, %v", base, got, err)
		}
	}
}

func TestRepositoryBaseCapturesCurrentWorktreeCommit(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	canonicalNested, err := filepath.EvalSymlinks(nested)
	if err != nil {
		t.Fatal(err)
	}
	common := t.TempDir()
	current := model.Manifest{Repository: model.Repository{ID: "repo"}, Checkout: model.Checkout{Claim: &model.WorkspaceClaim{Path: root}}}
	selected := coordinator.RepositorySourceResult{Repository: model.OwnedRepository{ID: "repo", Name: "project"}, GitDir: common}
	for _, base := range []string{"HEAD", "HEAD~1", "@", "@{-1}", "-", "ORIG_HEAD"} {
		g := &baseInspector{common: common}
		got, err := resolveRepositoryBase(context.Background(), base, nested, &current, selected, g)
		want := base
		if base == "-" {
			want = "@{-1}"
		}
		if err != nil || got != "captured-commit" || g.path != canonicalNested || g.revision != want || g.calls != 1 {
			t.Errorf("%s -> %s, %v, %+v", base, got, err, g)
		}
	}
}

func TestRepositoryBaseRejectsMissingOrMismatchedContext(t *testing.T) {
	root, common := t.TempDir(), t.TempDir()
	current := model.Manifest{Repository: model.Repository{ID: "repo"}, Checkout: model.Checkout{Claim: &model.WorkspaceClaim{Path: root}}}
	selected := coordinator.RepositorySourceResult{Repository: model.OwnedRepository{ID: "repo", Name: "project"}, GitDir: common}
	tests := []struct {
		name      string
		current   *model.Manifest
		cwd       string
		selected  coordinator.RepositorySourceResult
		inspector *baseInspector
	}{
		{"missing item", nil, root, selected, &baseInspector{common: common}},
		{"missing claim", &model.Manifest{Repository: current.Repository}, root, selected, &baseInspector{common: common}},
		{"other repository", &current, root, coordinator.RepositorySourceResult{Repository: model.OwnedRepository{ID: "other"}}, &baseInspector{common: common}},
		{"outside checkout", &current, t.TempDir(), selected, &baseInspector{common: common}},
		{"nested unrelated repository", &current, root, selected, &baseInspector{common: t.TempDir()}},
		{"failed Git lookup", &current, root, selected, &baseInspector{common: common, err: fmt.Errorf("unknown revision")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got, err := resolveRepositoryBase(context.Background(), "HEAD", test.cwd, test.current, test.selected, test.inspector); err == nil || got != "" {
				t.Fatalf("accepted %q, %v", got, err)
			}
		})
	}
}
