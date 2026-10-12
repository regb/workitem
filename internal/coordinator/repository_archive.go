package coordinator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

type SetRepositoryArchivedCommand struct {
	ID               string    `json:"id"`
	ProtocolVersion  int       `json:"protocol_version"`
	RepositoryID     string    `json:"repository_id"`
	PreviousArchived bool      `json:"previous_archived"`
	Archived         bool      `json:"archived"`
	CreatedAt        time.Time `json:"created_at"`
}

type SetRepositoryArchivedResult struct {
	Repository model.OwnedRepository `json:"repository"`
	Changed    bool                  `json:"changed"`
	Sequence   uint64                `json:"sequence"`
	Duplicate  bool                  `json:"duplicate,omitempty"`
}

type repositoryArchiveOutcome struct {
	Command SetRepositoryArchivedCommand `json:"repository_archive_command"`
	Result  SetRepositoryArchivedResult  `json:"repository_archive_result"`
}

func (d *Database) SetRepositoryArchived(command SetRepositoryArchivedCommand) (SetRepositoryArchivedResult, error) {
	if command.ID == "" || compactIdentifier(command.ID, 200) != command.ID || command.ProtocolVersion != ProtocolVersion || command.CreatedAt.IsZero() || !model.ValidID(command.RepositoryID) {
		return SetRepositoryArchivedResult{}, fmt.Errorf("valid repository archive identity, protocol, and timestamp are required")
	}
	var result SetRepositoryArchivedResult
	err := d.db.Update(func(tx *bolt.Tx) error {
		outcomes := tx.Bucket(bucketDomainCommands)
		if tx.Bucket(bucketCommands).Get([]byte(command.ID)) != nil {
			return fmt.Errorf("command id %s was already used by another command", command.ID)
		}
		if encoded := outcomes.Get([]byte(command.ID)); encoded != nil {
			var old repositoryArchiveOutcome
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
		if repo.ID != command.RepositoryID {
			return fmt.Errorf("repository record identity does not match its registry key")
		}
		repo, err := model.NormalizeOwnedRepository(repo)
		if err != nil {
			return err
		}
		if repo.Archived != command.PreviousArchived {
			return fmt.Errorf("repository archive state changed; retry with its current state")
		}
		result = SetRepositoryArchivedResult{Repository: repo, Changed: repo.Archived != command.Archived}
		if result.Changed {
			repo.Archived = command.Archived
			sequence := decodeUint64(tx.Bucket(bucketMeta).Get(keyGlobalSequence)) + 1
			payload, _ := json.Marshal(map[string]any{"repository_id": repo.ID, "name": repo.Name})
			eventType := "repository.restored"
			if repo.Archived {
				eventType = "repository.archived"
			}
			event := DomainEvent{Sequence: sequence, ID: domainEventID(command.ID, 0), Type: eventType, Timestamp: command.CreatedAt.UTC(), Actor: "user", CausationID: command.ID, Payload: payload}
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
		encoded, err := json.Marshal(repositoryArchiveOutcome{Command: command, Result: result})
		if err != nil {
			return err
		}
		return outcomes.Put([]byte(command.ID), encoded)
	})
	return result, err
}
