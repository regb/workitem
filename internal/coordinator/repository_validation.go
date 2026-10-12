package coordinator

import (
	"encoding/json"
	"fmt"
	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

// Creation alone is fenced by repository archive state. Existing items retain
// access to the repository and may continue all normal workflows.
func validateCreationRepositoryReference(tx *bolt.Tx, m model.Manifest) error {
	if err := validateRepositoryReference(tx, m); err != nil {
		return err
	}
	if m.Repository.ID == "" {
		return nil
	}
	var repo model.OwnedRepository
	raw := tx.Bucket(bucketProjections).Bucket([]byte(RepositoryProjection)).Get([]byte(m.Repository.ID))
	if err := json.Unmarshal(raw, &repo); err != nil {
		return err
	}
	if repo.Archived {
		return fmt.Errorf("repository %q is archived; restore it with wi repo restore %s before creating new items", repo.Name, repo.Name)
	}
	return nil
}

func validateRepositoryReference(tx *bolt.Tx, m model.Manifest) error {
	repositories := tx.Bucket(bucketProjections).Bucket([]byte(RepositoryProjection))
	if m.Repository.ID == "" {
		if repositories != nil && repositories.Stats().KeyN > 0 {
			return fmt.Errorf("work items require a registered repository identity")
		}
		return nil
	}
	if repositories == nil {
		return fmt.Errorf("repository %s is not registered", m.Repository.ID)
	}
	raw := repositories.Get([]byte(m.Repository.ID))
	if raw == nil {
		return fmt.Errorf("repository %s is not registered", m.Repository.ID)
	}
	var r model.OwnedRepository
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	if m.Checkout.Specification.Branch == r.IntegrationBranch {
		return fmt.Errorf("integration branch must remain unchecked out; use an item implementation branch")
	}
	return nil
}
