package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

const (
	RepositoryEnvironmentProjection = "repository_environment"
	MethodRepositoryEnvironment     = "repository_environment"
	MethodSetRepositoryEnvironment  = "repository_environment_set"
	maxRepositoryEnvironmentBytes   = 256 << 10
	maxRepositoryEnvironmentVars    = 500
)

type RepositoryEnvironmentRequest struct {
	Selector string `json:"selector"`
}

type RepositoryEnvironmentResult struct {
	Repository  model.OwnedRepository       `json:"repository"`
	Environment model.RepositoryEnvironment `json:"environment"`
}

// SetRepositoryEnvironmentCommand changes a repository's variables: with
// Replace, Set becomes the whole list; otherwise Set adds or updates and
// Unset removes. ExpectedRevision, when given, fences a stale change.
type SetRepositoryEnvironmentCommand struct {
	Selector         string         `json:"selector"`
	ExpectedRevision *uint64        `json:"expected_revision,omitempty"`
	Replace          bool           `json:"replace,omitempty"`
	Set              []model.EnvVar `json:"set,omitempty"`
	Unset            []string       `json:"unset,omitempty"`
}

func (c *Client) RepositoryEnvironment(ctx context.Context, selector string) (RepositoryEnvironmentResult, error) {
	var result RepositoryEnvironmentResult
	err := c.Call(ctx, MethodRepositoryEnvironment, RepositoryEnvironmentRequest{Selector: selector}, &result)
	return result, err
}

func (c *Client) SetRepositoryEnvironment(ctx context.Context, command SetRepositoryEnvironmentCommand) (RepositoryEnvironmentResult, error) {
	var result RepositoryEnvironmentResult
	err := c.Call(ctx, MethodSetRepositoryEnvironment, command, &result)
	return result, err
}

func readEnvironmentTx(tx *bolt.Tx, id string) (model.RepositoryEnvironment, error) {
	record := model.RepositoryEnvironment{RepositoryID: id, Vars: []model.EnvVar{}}
	bucket := tx.Bucket(bucketProjections).Bucket([]byte(RepositoryEnvironmentProjection))
	if bucket == nil {
		return record, nil
	}
	raw := bucket.Get([]byte(id))
	if raw == nil {
		return record, nil
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return record, fmt.Errorf("decode repository environment: %w", err)
	}
	if record.RepositoryID != id {
		return record, fmt.Errorf("repository environment identity mismatch")
	}
	if record.Vars == nil {
		record.Vars = []model.EnvVar{}
	}
	return record, nil
}

// RepositoryEnvironment reads a repository's variables, by name or ID.
func (d *Database) RepositoryEnvironment(selector string) (RepositoryEnvironmentResult, error) {
	settings, err := d.RepositorySettings(selector)
	if err != nil {
		return RepositoryEnvironmentResult{}, err
	}
	result := RepositoryEnvironmentResult{Repository: settings.Repository}
	err = d.db.View(func(tx *bolt.Tx) error {
		result.Environment, err = readEnvironmentTx(tx, settings.Repository.ID)
		return err
	})
	return result, err
}

// SetRepositoryEnvironment applies a change and records that the variables
// changed, naming keys but never values, which may be secrets.
func (d *Database) SetRepositoryEnvironment(command SetRepositoryEnvironmentCommand) (RepositoryEnvironmentResult, error) {
	if err := model.ValidEnv(command.Set); err != nil {
		return RepositoryEnvironmentResult{}, err
	}
	for _, key := range command.Unset {
		if err := model.ValidEnvKey(key); err != nil {
			return RepositoryEnvironmentResult{}, err
		}
	}
	settings, err := d.RepositorySettings(command.Selector)
	if err != nil {
		return RepositoryEnvironmentResult{}, err
	}
	id := settings.Repository.ID
	result := RepositoryEnvironmentResult{Repository: settings.Repository}
	err = d.db.Update(func(tx *bolt.Tx) error {
		environment, err := readEnvironmentTx(tx, id)
		if err != nil {
			return err
		}
		if command.ExpectedRevision != nil && *command.ExpectedRevision != environment.Revision {
			return fmt.Errorf("the environment of %s changed since it was read; reload and try again", settings.Repository.Name)
		}
		next := environment.Vars
		if command.Replace {
			next = command.Set
		} else {
			next = model.MergeEnv(next, command.Set, command.Unset)
		}
		if len(next) > maxRepositoryEnvironmentVars {
			return fmt.Errorf("a repository holds at most %d variables", maxRepositoryEnvironmentVars)
		}
		changed := model.EnvChanges(environment.Vars, next)
		environment.Vars = append([]model.EnvVar{}, next...)
		result.Environment = environment
		if len(changed) == 0 {
			return nil
		}
		environment.Revision++
		now := time.Now().UTC()
		environment.ChangedAt = &now
		data, err := json.Marshal(environment)
		if err != nil {
			return err
		}
		if len(data) > maxRepositoryEnvironmentBytes {
			return fmt.Errorf("repository environment exceeds 256 KiB")
		}
		bucket, err := tx.Bucket(bucketProjections).CreateBucketIfNotExists([]byte(RepositoryEnvironmentProjection))
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(id), data); err != nil {
			return err
		}
		result.Environment = environment
		payload, _ := json.Marshal(map[string]any{"repository_id": id, "keys": changed, "revision": environment.Revision})
		eventID := fmt.Sprintf("repository-environment-%s-%d", id, environment.Revision)
		_, err = appendDomainEventTx(tx, DomainEvent{ID: eventID, Type: "repository.environment_changed", Timestamp: now, Actor: "user", Payload: payload})
		return err
	})
	return result, err
}
