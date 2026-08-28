package coordinator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	bolt "go.etcd.io/bbolt"
)

const CommandItemCreate = "item.create"

type CreateItemCommand struct {
	ID                string         `json:"id"`
	ProtocolVersion   int            `json:"protocol_version"`
	Manifest          model.Manifest `json:"manifest"`
	DescriptionDigest string         `json:"description_digest"`
	Actor             string         `json:"actor"`
	CreatedAt         time.Time      `json:"created_at"`
}

type CreateItemRequest struct {
	Command     CreateItemCommand `json:"command"`
	Description string            `json:"description"`
}

type CreateItemResult struct {
	CommandID   string         `json:"command_id"`
	Manifest    model.Manifest `json:"manifest"`
	Revision    uint64         `json:"revision"`
	Event       DomainEvent    `json:"event"`
	Duplicate   bool           `json:"duplicate,omitempty"`
	CommittedAt time.Time      `json:"committed_at"`
}

type createCommandOutcome struct {
	Command CreateItemCommand `json:"create_command"`
	Result  CreateItemResult  `json:"create_result"`
}

func DescriptionDigest(description string) string {
	digest := sha256.Sum256([]byte(description))
	return hex.EncodeToString(digest[:])
}

func (d *Database) ExecuteCreateItemCommand(command CreateItemCommand) (CreateItemResult, error) {
	return d.executeCreateItemCommand(command, "user")
}

func (d *Database) ExecuteAgentCreateItemCommand(command CreateItemCommand) (CreateItemResult, error) {
	return d.executeCreateItemCommand(command, "agent")
}

func (d *Database) executeCreateItemCommand(command CreateItemCommand, actor string) (CreateItemResult, error) {
	if compactIdentifier(command.ID, 200) != command.ID || command.ID == "" {
		return CreateItemResult{}, errors.New("command id is invalid")
	}
	if command.ProtocolVersion != ProtocolVersion {
		return CreateItemResult{}, fmt.Errorf("unsupported command protocol %d", command.ProtocolVersion)
	}
	if command.Actor == "" {
		command.Actor = actor
	}
	if command.Actor != actor {
		return CreateItemResult{}, fmt.Errorf("item creation requires %s actor", actor)
	}
	digestBytes, digestErr := hex.DecodeString(command.DescriptionDigest)
	if digestErr != nil || len(digestBytes) != sha256.Size {
		return CreateItemResult{}, errors.New("description digest is invalid")
	}
	normalized, err := store.NormalizeManifest(command.Manifest)
	if err != nil {
		return CreateItemResult{}, err
	}
	command.Manifest = normalized
	if command.CreatedAt.IsZero() {
		command.CreatedAt = normalized.CreatedAt
	} else if !command.CreatedAt.Equal(normalized.CreatedAt) {
		return CreateItemResult{}, errors.New("command creation time must match manifest creation time")
	}
	if normalized.State != model.StateBacklog {
		return CreateItemResult{}, errors.New("new work item must start in backlog")
	}
	if model.Slugify(normalized.Slug) != normalized.Slug {
		return CreateItemResult{}, fmt.Errorf("invalid canonical slug %q", normalized.Slug)
	}
	var result CreateItemResult
	err = d.db.Update(func(tx *bolt.Tx) error {
		outcomes := tx.Bucket(bucketDomainCommands)
		if encoded := outcomes.Get([]byte(command.ID)); encoded != nil {
			var outcome createCommandOutcome
			if err := json.Unmarshal(encoded, &outcome); err != nil {
				return err
			}
			if outcome.Command.ID == "" || !sameCreateItemCommand(outcome.Command, command) {
				return fmt.Errorf("command id %s was already used with different input", command.ID)
			}
			result = outcome.Result
			result.Duplicate = true
			return nil
		}
		projection, err := tx.Bucket(bucketProjections).CreateBucketIfNotExists([]byte(ManifestProjection))
		if err != nil {
			return err
		}
		if projection.Get([]byte(normalized.ID)) != nil {
			return fmt.Errorf("work item id collision %s", normalized.ID)
		}
		manifests, err := manifestsFromProjection(projection)
		if err != nil {
			return err
		}
		if err := validateCanonicalCreateClaims(normalized, manifests); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"title": normalized.Title, "slug": normalized.Slug, "workspace_kind": normalized.Checkout.Kind})
		meta := tx.Bucket(bucketMeta)
		sequence := decodeUint64(meta.Get(keyGlobalSequence)) + 1
		eventID := domainEventID(command.ID, 0)
		event := DomainEvent{Sequence: sequence, ID: eventID, ItemID: normalized.ID, ItemRevision: 1, Type: "work_item.created", Timestamp: command.CreatedAt.UTC(), Actor: command.Actor, CausationID: command.ID, Payload: payload}
		encodedEvent, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if err := tx.Bucket(bucketEvents).Put(encodeUint64(sequence), encodedEvent); err != nil {
			return err
		}
		if err := tx.Bucket(bucketEventIDs).Put([]byte(eventID), encodeUint64(sequence)); err != nil {
			return err
		}
		manifestBytes, err := json.Marshal(normalized)
		if err != nil {
			return err
		}
		if err := projection.Put([]byte(normalized.ID), manifestBytes); err != nil {
			return err
		}
		if err := tx.Bucket(bucketRevisions).Put([]byte(normalized.ID), encodeUint64(1)); err != nil {
			return err
		}
		digest := sha256.Sum256(manifestBytes)
		if err := tx.Bucket(bucketImportSources).Put([]byte(normalized.ID), []byte(hex.EncodeToString(digest[:]))); err != nil {
			return err
		}
		pending, err := json.Marshal(pendingNativeWrite{CommandID: command.ID, Sequence: sequence, Operation: "create", Manifest: normalized})
		if err != nil {
			return err
		}
		if err := tx.Bucket(bucketPendingWrites).Put([]byte(command.ID), pending); err != nil {
			return err
		}
		if err := meta.Put(keyGlobalSequence, encodeUint64(sequence)); err != nil {
			return err
		}
		result = CreateItemResult{CommandID: command.ID, Manifest: normalized, Revision: 1, Event: event, CommittedAt: time.Now().UTC()}
		encodedOutcome, err := json.Marshal(createCommandOutcome{Command: command, Result: result})
		if err != nil {
			return err
		}
		return outcomes.Put([]byte(command.ID), encodedOutcome)
	})
	return result, err
}

func validateCanonicalCreateClaims(manifest model.Manifest, existing []model.Manifest) error {
	for _, other := range existing {
		if other.ID == manifest.ID {
			continue
		}
		if other.State != model.StateArchived && other.Slug == manifest.Slug {
			return fmt.Errorf("slug %q is already taken by active work item %s", manifest.Slug, other.ID)
		}
		if manifest.Checkout.Kind == model.WorkspaceKindRepositoryHome && manifest.Checkout.Present() &&
			other.Checkout.Kind == model.WorkspaceKindRepositoryHome && other.Checkout.Present() && sameRepositoryIdentity(manifest.Repository, other.Repository) {
			return fmt.Errorf("repository home is already claimed by work item %s (%s)", other.ID, other.Slug)
		}
	}
	return nil
}

func sameRepositoryIdentity(left, right model.Repository) bool {
	if left.GitCommonDir != "" && right.GitCommonDir != "" {
		return filepath.Clean(left.GitCommonDir) == filepath.Clean(right.GitCommonDir)
	}
	return filepath.Clean(left.RootAtCreation) == filepath.Clean(right.RootAtCreation)
}

func sameCreateItemCommand(left, right CreateItemCommand) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes)
}

func descriptionStagePath(database *Database, commandID string) string {
	digest := sha256.Sum256([]byte(commandID))
	return filepath.Join(filepath.Dir(database.path), ".coordinator-pending", hex.EncodeToString(digest[:])+".description")
}

func StageDescription(database *Database, commandID, description string) error {
	if len(description) > 4<<20 {
		return errors.New("description exceeds 4 MiB")
	}
	path := descriptionStagePath(database, commandID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".description-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.WriteString(description); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	cleanup = false
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func RemoveStagedDescription(database *Database, commandID string) {
	_ = os.Remove(descriptionStagePath(database, commandID))
}

func CleanupOrphanedStages(database *Database) error {
	directory := filepath.Join(filepath.Dir(database.path), ".coordinator-pending")
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func validateCreateDescription(command CreateItemCommand, description string) error {
	if DescriptionDigest(description) != strings.ToLower(command.DescriptionDigest) {
		return errors.New("description digest does not match command")
	}
	return nil
}
