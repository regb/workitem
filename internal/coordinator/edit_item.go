package coordinator

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

const MaxDescriptionBytes = 4 << 20

// MetadataEdit is a patch, never a replacement for lifecycle or resource intent.
// Native description content is staged on disk; only hashes enter the database.
type MetadataEdit struct {
	Title                     *string  `json:"title,omitempty"`
	AddLabels                 []string `json:"add_labels,omitempty"`
	RemoveLabels              []string `json:"remove_labels,omitempty"`
	DeepWork                  *bool    `json:"deep_work,omitempty"`
	DescriptionDigest         string   `json:"description_digest,omitempty"`
	PreviousDescriptionDigest string   `json:"previous_description_digest,omitempty"`
}
type EditItemRequest struct {
	Command     ManifestCommand `json:"command"`
	Description *string         `json:"description,omitempty"`
}

func ValidateEditTitle(title string) error {
	if title == "" || len(title) > 512 || !utf8.ValidString(title) || strings.IndexFunc(title, unicode.IsControl) >= 0 {
		return errors.New("a single-line UTF-8 title of up to 512 bytes is required")
	}
	return nil
}
func ValidateDescription(description string) error {
	if len(description) > MaxDescriptionBytes || !utf8.ValidString(description) {
		return errors.New("description must be UTF-8 and at most 4 MiB")
	}
	return nil
}
func validDescriptionDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == strings.ToLower(value)
}
func applyMetadataEdit(edit *MetadataEdit, m *model.Manifest) ([]string, error) {
	if edit == nil {
		return nil, errors.New("metadata edit is required")
	}
	fields := []string{}
	if edit.Title != nil {
		title := strings.TrimSpace(*edit.Title)
		if err := ValidateEditTitle(title); err != nil {
			return nil, err
		}
		if m.Title != title {
			m.Title = title
			fields = append(fields, "title")
		}
	}
	add, err := model.NormalizeLabels(edit.AddLabels)
	if err != nil {
		return nil, err
	}
	remove, err := model.NormalizeLabels(edit.RemoveLabels)
	if err != nil {
		return nil, err
	}
	for _, label := range add {
		if model.HasLabel(remove, label) {
			return nil, fmt.Errorf("label %q cannot be both added and removed", label)
		}
	}
	labels, _, err := model.RemoveLabels(m.Labels, remove...)
	if err != nil {
		return nil, err
	}
	labels, _, err = model.AddLabels(labels, add...)
	if err != nil {
		return nil, err
	}
	if strings.Join(labels, "\x00") != strings.Join(m.Labels, "\x00") {
		m.Labels = labels
		fields = append(fields, "labels")
	}
	if edit.DeepWork != nil && m.DeepWork != *edit.DeepWork {
		m.DeepWork = *edit.DeepWork
		fields = append(fields, "deep_work")
	}
	if edit.DescriptionDigest != "" {
		if !validDescriptionDigest(edit.DescriptionDigest) || !validDescriptionDigest(edit.PreviousDescriptionDigest) {
			return nil, errors.New("description edit requires valid old and new digests")
		}
		if edit.DescriptionDigest != edit.PreviousDescriptionDigest {
			fields = append(fields, "description")
		}
	} else if edit.PreviousDescriptionDigest != "" {
		return nil, errors.New("new description digest is required")
	}
	return fields, nil
}

func ReadEditDescription(native interface{ ItemDir(string) string }, itemID string) (string, error) {
	if !model.ValidID(itemID) {
		return "", errors.New("invalid item id")
	}
	dir := native.ItemDir(itemID)
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("item directory must not be a symlink")
	}
	path := filepath.Join(dir, model.DescriptionFilename)
	info, err = os.Lstat(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("description must be a regular file, not a symlink")
	}
	if info.Size() > MaxDescriptionBytes {
		return "", errors.New("description exceeds 4 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxDescriptionBytes+1))
	if err != nil {
		return "", err
	}
	if err := ValidateDescription(string(data)); err != nil {
		return "", err
	}
	return string(data), nil
}

// committedEdit detects replay before filesystem preconditions or staging.
func (d *Database) committedEdit(command ManifestCommand) (bool, error) {
	found := false
	err := d.db.View(func(tx *bolt.Tx) error {
		encoded := tx.Bucket(bucketDomainCommands).Get([]byte(command.ID))
		if encoded == nil {
			return nil
		}
		var outcome domainCommandOutcome
		if err := json.Unmarshal(encoded, &outcome); err != nil {
			return err
		}
		if !sameManifestCommand(command, outcome.Command) {
			return errors.New("command id was already used with different input")
		}
		found = true
		return nil
	})
	return found, err
}

func (s *Server) editItem(request EditItemRequest) (ManifestCommandResult, error) {
	command := request.Command
	if command.Type != CommandEdit || command.Edit == nil {
		return ManifestCommandResult{}, errors.New("edit_item requires an item.edit command")
	}
	// Validate complete patch before staging, and never accept content via the
	// ordinary manifest-command endpoint.
	dummy := model.Manifest{}
	if _, err := applyMetadataEdit(command.Edit, &dummy); err != nil {
		return ManifestCommandResult{}, err
	}
	if request.Description != nil {
		if err := ValidateDescription(*request.Description); err != nil {
			return ManifestCommandResult{}, err
		}
		if DescriptionDigest(*request.Description) != command.Edit.DescriptionDigest {
			return ManifestCommandResult{}, errors.New("description digest does not match content")
		}
	} else if command.Edit.DescriptionDigest != "" {
		return ManifestCommandResult{}, errors.New("description content is required")
	}
	committed, err := s.database.committedEdit(command)
	if err != nil {
		return ManifestCommandResult{}, err
	}
	if !committed {
		pending, err := s.database.PendingNativeWrites()
		if err != nil {
			return ManifestCommandResult{}, err
		}
		for _, write := range pending {
			if write.Manifest.ID == command.ItemID && write.Operation == "edit-description" {
				return ManifestCommandResult{}, fmt.Errorf("item has a pending description edit %s; reconcile its staged content first", write.CommandID)
			}
		}
		if request.Description != nil {
			current, err := ReadEditDescription(s.native, command.ItemID)
			if err != nil {
				return ManifestCommandResult{}, err
			}
			if DescriptionDigest(current) != command.Edit.PreviousDescriptionDigest {
				return ManifestCommandResult{}, errors.New("description changed during edit; retry without overwriting concurrent changes")
			}
			if err := StageDescription(s.database, command.ID, *request.Description); err != nil {
				return ManifestCommandResult{}, err
			}
		}
	}
	result, err := s.database.ExecuteManifestCommand(command)
	if err != nil {
		if !committed {
			RemoveStagedDescription(s.database, command.ID)
		}
		return ManifestCommandResult{}, err
	}
	if err := MaterializeNativeWrites(context.Background(), s.database, s.native); err != nil {
		return result, fmt.Errorf("edit %s committed; native write pending: %w", command.ID, err)
	}
	pending, err := s.database.PendingNativeWrites()
	if err != nil {
		return result, err
	}
	for _, write := range pending {
		if write.CommandID == command.ID {
			return result, fmt.Errorf("edit %s committed, but description write is pending; recover staged content at %s without overwriting concurrent changes", command.ID, descriptionStagePath(s.database, command.ID))
		}
	}
	return result, nil
}
