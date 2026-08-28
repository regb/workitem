package coordinator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

// MaterializeNativeWrites applies the on-disk side effects of committed domain
// commands that own unrestricted native content: item directories and
// descriptions and deletion.
// Startup calls it before accepting work so an interrupted command finishes its
// native write rather than being lost.
func MaterializeNativeWrites(ctx context.Context, database *Database, native *store.Store) error {
	pending, err := database.PendingNativeWrites()
	if err != nil {
		return err
	}
	for _, write := range pending {
		switch write.Operation {
		case "create":
			if err := materializeCreate(database, native, write); err != nil {
				return err
			}
			RemoveStagedDescription(database, write.CommandID)
		case StoreItemDelete:
			if err := native.RemoveItem(write.Manifest.ID); err != nil {
				return err
			}
			RemoveStagedStoreMutation(database, write.CommandID)
		default:
			RemoveStagedStoreMutation(database, write.CommandID)
		}
		if err := database.ClearPendingNativeWrite(write.CommandID); err != nil {
			return err
		}
	}
	return nil
}

func materializeCreate(database *Database, native *store.Store, write pendingNativeWrite) error {
	itemDir := native.ItemDir(write.Manifest.ID)
	description, err := os.ReadFile(descriptionStagePath(database, write.CommandID))
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		description, err = os.ReadFile(filepath.Join(itemDir, model.DescriptionFilename))
		if err != nil {
			return fmt.Errorf("recover native description for %s: %w", write.Manifest.ID, err)
		}
	}
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		return err
	}
	for _, relative := range []string{filepath.Join("sessions", "pi"), "locks"} {
		if err := os.MkdirAll(filepath.Join(itemDir, relative), 0o700); err != nil {
			return err
		}
	}
	descriptionPath := filepath.Join(itemDir, model.DescriptionFilename)
	if _, err := os.Lstat(descriptionPath); os.IsNotExist(err) {
		if err := os.WriteFile(descriptionPath, description, 0o600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return nil
}
