package coordinator

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

const RepositoryProjection = "repositories"

type RegisterRepositoryCommand struct {
	ID              string                `json:"id"`
	ProtocolVersion int                   `json:"protocol_version"`
	Repository      model.OwnedRepository `json:"repository"`
	Source          string                `json:"source"`
	CreatedAt       time.Time             `json:"created_at"`
}

type RegisterRepositoryResult struct {
	Repository model.OwnedRepository `json:"repository"`
	Sequence   uint64                `json:"sequence"`
	Duplicate  bool                  `json:"duplicate,omitempty"`
}

type repositoryCommandOutcome struct {
	Command RegisterRepositoryCommand `json:"repository_command"`
	Result  RegisterRepositoryResult  `json:"repository_result"`
}

// ListRepositories returns the complete registry, including archived records,
// so names remain reserved and existing item references continue to resolve.
func (d *Database) ListRepositories() ([]model.OwnedRepository, error) {
	raw, err := d.ListProjection(RepositoryProjection)
	if err != nil {
		return nil, err
	}
	result := make([]model.OwnedRepository, 0, len(raw))
	for _, v := range raw {
		var r model.OwnedRepository
		if err := json.Unmarshal(v, &r); err != nil {
			return nil, err
		}
		normalized, err := model.NormalizeOwnedRepository(r)
		if err != nil {
			return nil, err
		}
		result = append(result, normalized)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (d *Database) RegisterRepository(command RegisterRepositoryCommand) (RegisterRepositoryResult, error) {
	if command.ID == "" || compactIdentifier(command.ID, 200) != command.ID || command.ProtocolVersion != ProtocolVersion || command.CreatedAt.IsZero() {
		return RegisterRepositoryResult{}, fmt.Errorf("valid repository command identity, protocol, and timestamp are required")
	}
	r, err := model.NormalizeOwnedRepository(command.Repository)
	if err != nil {
		return RegisterRepositoryResult{}, err
	}
	if r.Archived {
		return RegisterRepositoryResult{}, fmt.Errorf("new repositories must be active")
	}
	command.Repository = r
	if err := model.ValidateRepositorySource(command.Source); err != nil {
		return RegisterRepositoryResult{}, err
	}
	source := command.Source
	var result RegisterRepositoryResult
	err = d.db.Update(func(tx *bolt.Tx) error {
		outcomes := tx.Bucket(bucketDomainCommands)
		if tx.Bucket(bucketCommands).Get([]byte(command.ID)) != nil {
			return fmt.Errorf("command id %s was already used by another command", command.ID)
		}
		if encoded := outcomes.Get([]byte(command.ID)); encoded != nil {
			var old repositoryCommandOutcome
			if err := json.Unmarshal(encoded, &old); err != nil {
				return err
			}
			a, _ := json.Marshal(old.Command)
			b, _ := json.Marshal(command)
			if string(a) != string(b) {
				return fmt.Errorf("command id %s was already used with different input", command.ID)
			}
			result = old.Result
			result.Duplicate = true
			return nil
		}
		bucket, err := tx.Bucket(bucketProjections).CreateBucketIfNotExists([]byte(RepositoryProjection))
		if err != nil {
			return err
		}
		if bucket.Get([]byte(r.ID)) != nil {
			return fmt.Errorf("repository id %s already exists", r.ID)
		}
		if err := bucket.ForEach(func(_, v []byte) error {
			var existing model.OwnedRepository
			if err := json.Unmarshal(v, &existing); err != nil {
				return err
			}
			if existing.Name == r.Name {
				return fmt.Errorf("repository name %q already exists", r.Name)
			}
			return nil
		}); err != nil {
			return err
		}
		sequence := decodeUint64(tx.Bucket(bucketMeta).Get(keyGlobalSequence)) + 1
		payload, _ := json.Marshal(map[string]any{"repository_id": r.ID, "name": r.Name, "integration_branch": r.IntegrationBranch, "source": source})
		event := DomainEvent{Sequence: sequence, ID: domainEventID(command.ID, 0), Type: "repository.onboarded", Timestamp: command.CreatedAt.UTC(), Actor: "user", CausationID: command.ID, Payload: payload}
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
		value, _ := json.Marshal(r)
		if err := bucket.Put([]byte(r.ID), value); err != nil {
			return err
		}
		result = RegisterRepositoryResult{Repository: r, Sequence: sequence}
		outcome, _ := json.Marshal(repositoryCommandOutcome{Command: command, Result: result})
		return outcomes.Put([]byte(command.ID), outcome)
	})
	return result, err
}
