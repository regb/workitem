package coordinator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

type RenameRepositoryCommand struct {
	ID              string    `json:"id"`
	ProtocolVersion int       `json:"protocol_version"`
	RepositoryID    string    `json:"repository_id"`
	PreviousName    string    `json:"previous_name"`
	Name            string    `json:"name"`
	CreatedAt       time.Time `json:"created_at"`
}

type RenameRepositoryResult struct {
	Repository   model.OwnedRepository `json:"repository"`
	PreviousName string                `json:"previous_name"`
	Changed      bool                  `json:"changed"`
	Sequence     uint64                `json:"sequence"`
	Duplicate    bool                  `json:"duplicate,omitempty"`
}

type repositoryRenameOutcome struct {
	Command RenameRepositoryCommand `json:"repository_rename_command"`
	Result  RenameRepositoryResult  `json:"repository_rename_result"`
}

func (d *Database) RenameRepository(command RenameRepositoryCommand) (RenameRepositoryResult, error) {
	if command.ID == "" || compactIdentifier(command.ID, 200) != command.ID || command.ProtocolVersion != ProtocolVersion || command.CreatedAt.IsZero() || !model.ValidID(command.RepositoryID) {
		return RenameRepositoryResult{}, fmt.Errorf("valid repository rename identity, protocol, and timestamp are required")
	}
	if !model.ValidRepositoryName(command.Name) || !model.ValidRepositoryName(command.PreviousName) {
		return RenameRepositoryResult{}, fmt.Errorf("repository names must be 1–64 lowercase letters, digits, dots, underscores, or hyphens and start with a letter or digit")
	}
	var result RenameRepositoryResult
	err := d.db.Update(func(tx *bolt.Tx) error {
		outcomes := tx.Bucket(bucketDomainCommands)
		if tx.Bucket(bucketCommands).Get([]byte(command.ID)) != nil {
			return fmt.Errorf("command id %s was already used by another command", command.ID)
		}
		if encoded := outcomes.Get([]byte(command.ID)); encoded != nil {
			var old repositoryRenameOutcome
			if err := json.Unmarshal(encoded, &old); err != nil {
				return err
			}
			a, _ := json.Marshal(old.Command)
			b, _ := json.Marshal(command)
			if !bytes.Equal(a, b) {
				return fmt.Errorf("command id %s was already used with different input", command.ID)
			}
			result = old.Result
			result.Duplicate = true
			return nil
		}
		bucket := tx.Bucket(bucketProjections).Bucket([]byte(RepositoryProjection))
		if bucket == nil || bucket.Get([]byte(command.RepositoryID)) == nil {
			return fmt.Errorf("repository %s is not registered", command.RepositoryID)
		}
		var repo model.OwnedRepository
		if err := json.Unmarshal(bucket.Get([]byte(command.RepositoryID)), &repo); err != nil {
			return err
		}
		if repo.Name != command.PreviousName {
			return fmt.Errorf("repository name changed from %s to %s; retry with its current name", command.PreviousName, repo.Name)
		}
		if err := bucket.ForEach(func(key, value []byte) error {
			if string(key) == repo.ID {
				return nil
			}
			var other model.OwnedRepository
			if err := json.Unmarshal(value, &other); err != nil {
				return err
			}
			if other.Name == command.Name {
				return fmt.Errorf("repository name %q already exists", command.Name)
			}
			return nil
		}); err != nil {
			return err
		}
		result = RenameRepositoryResult{Repository: repo, PreviousName: repo.Name, Changed: repo.Name != command.Name}
		if result.Changed {
			repo.Name = command.Name
			var err error
			repo, err = model.NormalizeOwnedRepository(repo)
			if err != nil {
				return err
			}
			sequence := decodeUint64(tx.Bucket(bucketMeta).Get(keyGlobalSequence)) + 1
			payload, _ := json.Marshal(map[string]any{"repository_id": repo.ID, "previous_name": command.PreviousName, "name": repo.Name})
			event := DomainEvent{Sequence: sequence, ID: domainEventID(command.ID, 0), Type: "repository.renamed", Timestamp: command.CreatedAt.UTC(), Actor: "user", CausationID: command.ID, Payload: payload}
			if tx.Bucket(bucketEventIDs).Get([]byte(event.ID)) != nil {
				return fmt.Errorf("event identity %s already exists", event.ID)
			}
			encoded, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if err := tx.Bucket(bucketEvents).Put(encodeUint64(sequence), encoded); err != nil {
				return err
			}
			if err := tx.Bucket(bucketEventIDs).Put([]byte(event.ID), encodeUint64(sequence)); err != nil {
				return err
			}
			if err := tx.Bucket(bucketMeta).Put(keyGlobalSequence, encodeUint64(sequence)); err != nil {
				return err
			}
			value, err := json.Marshal(repo)
			if err != nil {
				return err
			}
			if err := bucket.Put([]byte(repo.ID), value); err != nil {
				return err
			}
			result.Repository = repo
			result.Sequence = sequence
		}
		encoded, err := json.Marshal(repositoryRenameOutcome{Command: command, Result: result})
		if err != nil {
			return err
		}
		return outcomes.Put([]byte(command.ID), encoded)
	})
	if err == nil {
		// Repository changes show in every item's project.
		d.changes.recordAll()
	}
	return result, err
}
