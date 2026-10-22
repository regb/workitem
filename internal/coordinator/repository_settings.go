package coordinator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/options"
	bolt "go.etcd.io/bbolt"
)

const RepositorySettingsProjection = "repository_settings"
const maxRepositorySettingsBytes = 64 << 10

type RepositorySettingsRequest struct {
	Selector string `json:"selector"`
}
type RepositorySettingsResult struct {
	Repository model.OwnedRepository    `json:"repository"`
	Settings   model.RepositorySettings `json:"settings"`
}
type SetRepositorySettingCommand struct {
	ID               string          `json:"id"`
	ProtocolVersion  int             `json:"protocol_version"`
	RepositoryID     string          `json:"repository_id"`
	ExpectedRevision uint64          `json:"expected_revision"`
	Namespace        string          `json:"namespace"`
	Key              string          `json:"key"`
	Value            json.RawMessage `json:"value,omitempty"`
	Unset            bool            `json:"unset,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
}
type SetRepositorySettingResult struct {
	RepositoryID string `json:"repository_id"`
	Namespace    string `json:"namespace"`
	Key          string `json:"key"`
	Revision     uint64 `json:"revision"`
	Changed      bool   `json:"changed"`
	Sequence     uint64 `json:"sequence"`
	Duplicate    bool   `json:"duplicate,omitempty"`
}
type repositorySettingOutcome struct {
	Digest string                     `json:"repository_setting_digest"`
	Result SetRepositorySettingResult `json:"repository_setting_result"`
}

func readSettingsTx(tx *bolt.Tx, id string) (model.RepositorySettings, error) {
	record := model.RepositorySettings{RepositoryID: id, Namespaces: map[string]map[string]json.RawMessage{}}
	bucket := tx.Bucket(bucketProjections).Bucket([]byte(RepositorySettingsProjection))
	if bucket == nil {
		return record, nil
	}
	raw := bucket.Get([]byte(id))
	if raw == nil {
		return record, nil
	}
	if len(raw) > maxRepositorySettingsBytes {
		return record, fmt.Errorf("repository settings exceed storage bound")
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return record, fmt.Errorf("decode repository settings: %w", err)
	}
	if record.RepositoryID != id || record.Namespaces == nil || record.Revision == 0 {
		return record, fmt.Errorf("invalid repository settings identity or namespaces")
	}
	for namespace, entries := range record.Namespaces {
		if len(entries) == 0 {
			return record, fmt.Errorf("invalid empty repository settings namespace")
		}
		for key, value := range entries {
			if _, _, err := options.SplitRepositoryKey(namespace + "." + key); err != nil {
				return record, fmt.Errorf("invalid stored repository settings key")
			}
			if len(value) == 0 || len(value) > 8<<10 {
				return record, fmt.Errorf("invalid stored repository setting size")
			}
			if _, err := options.DecodeValue(value); err != nil {
				return record, fmt.Errorf("invalid stored repository setting JSON")
			}
		}
	}
	return record, nil
}
func (d *Database) RepositorySettings(selector string) (RepositorySettingsResult, error) {
	var result RepositorySettingsResult
	if !model.ValidID(selector) && !model.ValidRepositoryName(selector) {
		return result, fmt.Errorf("valid repository name or ID is required")
	}
	err := d.db.View(func(tx *bolt.Tx) error {
		repos := tx.Bucket(bucketProjections).Bucket([]byte(RepositoryProjection))
		if repos == nil {
			return fmt.Errorf("repository %q is not registered", selector)
		}
		found := false
		if err := repos.ForEach(func(key, value []byte) error {
			var repo model.OwnedRepository
			if err := json.Unmarshal(value, &repo); err != nil {
				return err
			}
			if repo.ID == selector || repo.Name == selector {
				normalized, err := model.NormalizeOwnedRepository(repo)
				if err != nil {
					return err
				}
				if string(key) != repo.ID {
					return fmt.Errorf("repository registry identity mismatch")
				}
				result.Repository = normalized
				found = true
			}
			return nil
		}); err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("repository %q is not registered", selector)
		}
		var err error
		result.Settings, err = readSettingsTx(tx, result.Repository.ID)
		return err
	})
	return result, err
}
func (d *Database) SetRepositorySetting(command SetRepositorySettingCommand, registry *options.Registry) (SetRepositorySettingResult, error) {
	var result SetRepositorySettingResult
	if command.ID == "" || compactIdentifier(command.ID, 200) != command.ID || command.ProtocolVersion != ProtocolVersion || command.CreatedAt.IsZero() || !model.ValidID(command.RepositoryID) {
		return result, fmt.Errorf("valid repository setting command identity, protocol, and timestamp are required")
	}
	if _, _, err := options.SplitRepositoryKey(command.Namespace + "." + command.Key); err != nil {
		return result, err
	}
	if command.Unset && len(command.Value) != 0 {
		return result, fmt.Errorf("unset cannot contain a value")
	}
	if !command.Unset {
		if len(command.Value) == 0 || len(command.Value) > 8<<10 {
			return result, fmt.Errorf("setting value must be nonempty JSON within 8 KiB")
		}
		value, err := options.DecodeValue(command.Value)
		if err != nil {
			return result, err
		}
		canonical, err := json.Marshal(value)
		if err != nil {
			return result, err
		}
		if len(canonical) > 8<<10 {
			return result, fmt.Errorf("canonical setting value exceeds 8 KiB")
		}
		command.Value = canonical
	}
	encoded, _ := json.Marshal(command)
	digestBytes := sha256.Sum256(encoded)
	digest := hex.EncodeToString(digestBytes[:])
	err := d.db.Update(func(tx *bolt.Tx) error {
		outcomes := tx.Bucket(bucketDomainCommands)
		if tx.Bucket(bucketCommands).Get([]byte(command.ID)) != nil {
			return fmt.Errorf("command id %s was already used by another command", command.ID)
		}
		if data := outcomes.Get([]byte(command.ID)); data != nil {
			var outcome repositorySettingOutcome
			if err := json.Unmarshal(data, &outcome); err != nil {
				return err
			}
			if outcome.Digest != digest {
				return fmt.Errorf("command id %s was already used with different input", command.ID)
			}
			result = outcome.Result
			result.Duplicate = true
			return nil
		}
		repositories := tx.Bucket(bucketProjections).Bucket([]byte(RepositoryProjection))
		if repositories == nil || repositories.Get([]byte(command.RepositoryID)) == nil {
			return fmt.Errorf("repository %s is not registered", command.RepositoryID)
		}
		var repo model.OwnedRepository
		if err := json.Unmarshal(repositories.Get([]byte(command.RepositoryID)), &repo); err != nil {
			return err
		}
		if repo.ID != command.RepositoryID {
			return fmt.Errorf("repository registry identity mismatch")
		}
		if _, err := model.NormalizeOwnedRepository(repo); err != nil {
			return err
		}
		settings, err := readSettingsTx(tx, repo.ID)
		if err != nil {
			return err
		}
		if settings.Revision != command.ExpectedRevision {
			return fmt.Errorf("repository settings changed concurrently; inspect and retry with the current revision")
		}
		if !command.Unset {
			value, err := options.DecodeValue(command.Value)
			if err != nil {
				return err
			}
			if err := registry.ValidateRepositorySetting(command.Namespace, command.Key, value); err != nil {
				return err
			}
		}
		entries := settings.Namespaces[command.Namespace]
		old, exists := entries[command.Key]
		changed := exists
		if !command.Unset {
			changed = !exists || !bytes.Equal(old, command.Value)
		}
		result = SetRepositorySettingResult{RepositoryID: repo.ID, Namespace: command.Namespace, Key: command.Key, Revision: settings.Revision, Changed: changed}
		if changed {
			if settings.Revision == ^uint64(0) {
				return fmt.Errorf("repository settings revision exhausted")
			}
			settings.Revision++
			if command.Unset {
				delete(entries, command.Key)
				if len(entries) == 0 {
					delete(settings.Namespaces, command.Namespace)
				}
			} else {
				if entries == nil {
					entries = map[string]json.RawMessage{}
					settings.Namespaces[command.Namespace] = entries
				}
				entries[command.Key] = command.Value
			}
			data, err := json.Marshal(settings)
			if err != nil {
				return err
			}
			if len(data) > maxRepositorySettingsBytes {
				return fmt.Errorf("repository settings exceed 64 KiB")
			}
			bucket, err := tx.Bucket(bucketProjections).CreateBucketIfNotExists([]byte(RepositorySettingsProjection))
			if err != nil {
				return err
			}
			if err := bucket.Put([]byte(repo.ID), data); err != nil {
				return err
			}
			sequence := decodeUint64(tx.Bucket(bucketMeta).Get(keyGlobalSequence))
			if sequence == ^uint64(0) {
				return fmt.Errorf("global event sequence exhausted")
			}
			sequence++
			// Values may contain secrets. History records the fact, never their contents.
			payload, _ := json.Marshal(map[string]any{"repository_id": repo.ID, "namespace": command.Namespace, "key": command.Key, "unset": command.Unset, "revision": settings.Revision})
			event := DomainEvent{Sequence: sequence, ID: domainEventID(command.ID, 0), Type: "repository.setting_changed", Timestamp: command.CreatedAt.UTC(), Actor: "user", CausationID: command.ID, Payload: payload}
			if tx.Bucket(bucketEventIDs).Get([]byte(event.ID)) != nil {
				return fmt.Errorf("event identity already exists")
			}
			data, err = json.Marshal(event)
			if err != nil {
				return err
			}
			if err := tx.Bucket(bucketEvents).Put(encodeUint64(sequence), data); err != nil {
				return err
			}
			if err := tx.Bucket(bucketEventIDs).Put([]byte(event.ID), encodeUint64(sequence)); err != nil {
				return err
			}
			if err := tx.Bucket(bucketMeta).Put(keyGlobalSequence, encodeUint64(sequence)); err != nil {
				return err
			}
			result.Revision = settings.Revision
			result.Sequence = sequence
		}
		data, err := json.Marshal(repositorySettingOutcome{Digest: digest, Result: result})
		if err != nil {
			return err
		}
		return outcomes.Put([]byte(command.ID), data)
	})
	if err == nil {
		// Repository changes show in every item's project.
		d.changes.recordAll()
	}
	return result, err
}
func (c *Client) RepositorySettings(ctx context.Context, selector string) (RepositorySettingsResult, error) {
	var result RepositorySettingsResult
	err := c.Call(ctx, MethodRepositorySettings, RepositorySettingsRequest{Selector: selector}, &result)
	return result, err
}
func (c *Client) SetRepositorySetting(ctx context.Context, command SetRepositorySettingCommand) (SetRepositorySettingResult, error) {
	var result SetRepositorySettingResult
	err := c.Call(ctx, MethodSetRepositorySetting, command, &result)
	return result, err
}
