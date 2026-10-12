package coordinator

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

func materializeDescriptionEdit(database *Database, native *store.Store, write pendingNativeWrite) error {
	current, err := ReadEditDescription(native, write.Manifest.ID)
	if err != nil {
		return err
	}
	digest := DescriptionDigest(current)
	if digest == write.DescriptionDigest {
		return nil
	} // rename succeeded before interruption
	if digest != write.PreviousDescriptionDigest {
		return fmt.Errorf("description changed concurrently; staged content retained at %s", descriptionStagePath(database, write.CommandID))
	}
	content, err := os.ReadFile(descriptionStagePath(database, write.CommandID))
	if err != nil {
		return err
	}
	if DescriptionDigest(string(content)) != write.DescriptionDigest {
		return fmt.Errorf("staged description digest mismatch")
	}
	dir := native.ItemDir(write.Manifest.ID)
	temp, err := os.CreateTemp(dir, ".edit-description-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	defer temp.Close()
	if err := temp.Chmod(0600); err != nil {
		return err
	}
	if _, err := temp.Write(content); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	// Check again after preparing the replacement, preserving external edits.
	latest, err := ReadEditDescription(native, write.Manifest.ID)
	if err != nil {
		return err
	}
	if DescriptionDigest(latest) != write.PreviousDescriptionDigest {
		return fmt.Errorf("description changed concurrently; staged content retained")
	}
	if err := os.Rename(name, filepath.Join(dir, model.DescriptionFilename)); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
