package app

import (
	"context"
	"path/filepath"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/repofiles"
)

// SyncRepositoryFiles brings a repository's checkouts in line with its
// files after one was added or removed: claimed checkouts keep their copies
// until their next claim, the others lose unedited copies of removed files
// at once, and the store's info/exclude lists what Git must ignore.
func (a *App) SyncRepositoryFiles(ctx context.Context, repositoryID string) ([]string, error) {
	dataRoot := filepath.Dir(a.Store.WorktreesDir())
	root, err := model.OwnedRepositoryGitDir(dataRoot, repositoryID)
	if err != nil {
		return nil, err
	}
	manifests, _ := a.Store.ListManifests()
	inUse := map[string]bool{}
	for _, m := range manifests {
		if m.Repository.ID == repositoryID && m.Checkout.Claim != nil && m.Checkout.Claim.Path != "" {
			inUse[m.Checkout.Claim.Path] = true
		}
	}
	return repofiles.Sync(ctx, root, a.Store.RepositoryFilesDir(repositoryID), inUse)
}
