package coordinator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	bolt "go.etcd.io/bbolt"
)

const (
	RuntimeOwnershipProjection  = "runtime_ownership"
	TerminalOwnershipProjection = "terminal_ownership"
	StoreManifestSave           = "manifest.save"
	StoreEventAppend            = "event.append"
	StoreTerminalRuntimeSave    = "terminal_runtime.save"
	StoreTerminalRuntimeRemove  = "terminal_runtime.remove"
	StoreAgentRuntimeSave       = "agent_runtime.save"
	StoreItemDelete             = "item.delete"
)

type StoreCommand struct {
	ID               string    `json:"id"`
	ProtocolVersion  int       `json:"protocol_version"`
	Operation        string    `json:"operation"`
	ItemID           string    `json:"item_id"`
	ExpectedRevision *uint64   `json:"expected_revision"`
	PayloadDigest    string    `json:"payload_digest"`
	CreatedAt        time.Time `json:"created_at"`
}

type StoreCommandRequest struct {
	Command StoreCommand    `json:"command"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type StoreCommandResult struct {
	CommandID   string      `json:"command_id"`
	Revision    uint64      `json:"revision"`
	Event       DomainEvent `json:"event"`
	Duplicate   bool        `json:"duplicate,omitempty"`
	CommittedAt time.Time   `json:"committed_at"`
}

type storeCommandOutcome struct {
	Command StoreCommand       `json:"store_command"`
	Result  StoreCommandResult `json:"store_result"`
}

func StorePayloadDigest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func (d *Database) ExecuteStoreCommand(command StoreCommand, payload json.RawMessage) (StoreCommandResult, error) {
	if command.ID == "" || compactIdentifier(command.ID, 200) != command.ID || !model.ValidID(command.ItemID) || command.ExpectedRevision == nil {
		return StoreCommandResult{}, errors.New("valid command id, item id, and expected revision are required")
	}
	if command.ProtocolVersion != ProtocolVersion {
		return StoreCommandResult{}, fmt.Errorf("unsupported command protocol %d", command.ProtocolVersion)
	}
	if StorePayloadDigest(payload) != command.PayloadDigest {
		return StoreCommandResult{}, errors.New("store command payload digest mismatch")
	}
	if command.CreatedAt.IsZero() {
		command.CreatedAt = time.Now().UTC()
	}
	var result StoreCommandResult
	err := d.db.Update(func(tx *bolt.Tx) error {
		outcomes := tx.Bucket(bucketDomainCommands)
		if encoded := outcomes.Get([]byte(command.ID)); encoded != nil {
			var outcome storeCommandOutcome
			if err := json.Unmarshal(encoded, &outcome); err != nil {
				return err
			}
			if outcome.Command.ID == "" || !sameStoreCommand(outcome.Command, command) {
				return fmt.Errorf("command id %s was already used with different input", command.ID)
			}
			result = outcome.Result
			result.Duplicate = true
			return nil
		}
		projection := tx.Bucket(bucketProjections).Bucket([]byte(ManifestProjection))
		if projection == nil || projection.Get([]byte(command.ItemID)) == nil {
			return store.ErrNotFound
		}
		revisions := tx.Bucket(bucketRevisions)
		revision := decodeUint64(revisions.Get([]byte(command.ItemID)))
		if revision != *command.ExpectedRevision {
			return fmt.Errorf("item revision conflict: expected %d, current %d", *command.ExpectedRevision, revision)
		}
		eventType, compactPayload, err := applyCanonicalStoreMutation(tx, command, payload)
		if err != nil {
			return err
		}
		eventTimestamp := command.CreatedAt.UTC()
		if command.Operation == StoreEventAppend {
			var appended model.Event
			if json.Unmarshal(payload, &appended) == nil && !appended.Time.IsZero() {
				eventTimestamp = appended.Time.UTC()
			}
		}
		sequence := decodeUint64(tx.Bucket(bucketMeta).Get(keyGlobalSequence)) + 1
		revision++
		eventID := domainEventID(command.ID, 0)
		event := DomainEvent{Sequence: sequence, ID: eventID, ItemID: command.ItemID, ItemRevision: revision, Type: eventType, Timestamp: eventTimestamp, Actor: "wi", CausationID: command.ID, Payload: compactPayload}
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
		if err := revisions.Put([]byte(command.ItemID), encodeUint64(revision)); err != nil {
			return err
		}
		if err := tx.Bucket(bucketMeta).Put(keyGlobalSequence, encodeUint64(sequence)); err != nil {
			return err
		}
		if requiresNativeWrite(command.Operation) {
			pending, err := json.Marshal(pendingNativeWrite{CommandID: command.ID, Sequence: sequence, Operation: command.Operation, Manifest: model.Manifest{ID: command.ItemID}})
			if err != nil {
				return err
			}
			if err := tx.Bucket(bucketPendingWrites).Put([]byte(command.ID), pending); err != nil {
				return err
			}
		}
		result = StoreCommandResult{CommandID: command.ID, Revision: revision, Event: event, CommittedAt: time.Now().UTC()}
		encodedOutcome, err := json.Marshal(storeCommandOutcome{Command: command, Result: result})
		if err != nil {
			return err
		}
		return outcomes.Put([]byte(command.ID), encodedOutcome)
	})
	return result, err
}

func applyCanonicalStoreMutation(tx *bolt.Tx, command StoreCommand, payload json.RawMessage) (string, json.RawMessage, error) {
	compact, _ := json.Marshal(map[string]any{"operation": command.Operation, "payload_digest": command.PayloadDigest})
	projection := tx.Bucket(bucketProjections).Bucket([]byte(ManifestProjection))
	switch command.Operation {
	case StoreManifestSave:
		var manifest model.Manifest
		if err := json.Unmarshal(payload, &manifest); err != nil {
			return "", nil, err
		}
		normalized, err := store.NormalizeManifest(manifest)
		if err != nil {
			return "", nil, err
		}
		if normalized.ID != command.ItemID {
			return "", nil, errors.New("manifest item id mismatch")
		}
		all, err := manifestsFromProjection(projection)
		if err != nil {
			return "", nil, err
		}
		if err := validateCanonicalCreateClaims(normalized, all); err != nil {
			return "", nil, err
		}
		encoded, _ := json.Marshal(normalized)
		if err := projection.Put([]byte(command.ItemID), encoded); err != nil {
			return "", nil, err
		}
		digest := sha256.Sum256(encoded)
		if err := tx.Bucket(bucketImportSources).Put([]byte(command.ItemID), []byte(hex.EncodeToString(digest[:]))); err != nil {
			return "", nil, err
		}
		return "manifest.updated", compact, nil
	case StoreEventAppend:
		var event model.Event
		if err := json.Unmarshal(payload, &event); err != nil {
			return "", nil, err
		}
		typeName := compactIdentifier(event.Type, 128)
		if typeName == "" {
			return "", nil, errors.New("event type is required")
		}
		compact, _ = json.Marshal(map[string]any{"legacy_event_type": typeName})
		if typeName == "attention.deferred" {
			activity := AttentionActivity{WorkItemID: command.ItemID, LastDeferredAt: ptrTime(event.Time.UTC())}
			if bucket, err := tx.Bucket(bucketProjections).CreateBucketIfNotExists([]byte(AttentionActivityProjection)); err == nil {
				if current := bucket.Get([]byte(command.ItemID)); current != nil {
					_ = json.Unmarshal(current, &activity)
					activity.LastDeferredAt = ptrTime(event.Time.UTC())
				}
				encoded, _ := json.Marshal(activity)
				_ = bucket.Put([]byte(command.ItemID), encoded)
			}
		}
		return typeName, compact, nil
	case StoreAgentRuntimeSave:
		var runtime model.AgentRuntime
		if err := json.Unmarshal(payload, &runtime); err != nil {
			return "", nil, err
		}
		if runtime.WorkItemID != command.ItemID {
			return "", nil, errors.New("runtime item id mismatch")
		}
		if err := putProjectionValue(tx, RuntimeOwnershipProjection, command.ItemID, runtime); err != nil {
			return "", nil, err
		}
		return command.Operation, compact, nil
	case StoreTerminalRuntimeSave:
		var terminal model.TerminalRuntime
		if err := json.Unmarshal(payload, &terminal); err != nil {
			return "", nil, err
		}
		terminal.TmuxPanePath = ""
		if err := putProjectionValue(tx, TerminalOwnershipProjection, command.ItemID, terminal); err != nil {
			return "", nil, err
		}
		return command.Operation, compact, nil
	case StoreTerminalRuntimeRemove:
		if bucket := tx.Bucket(bucketProjections).Bucket([]byte(TerminalOwnershipProjection)); bucket != nil {
			_ = bucket.Delete([]byte(command.ItemID))
		}
		return command.Operation, compact, nil
	case StoreItemDelete:
		var manifest model.Manifest
		if err := json.Unmarshal(projection.Get([]byte(command.ItemID)), &manifest); err != nil {
			return "", nil, err
		}
		if manifest.State != model.StateArchived {
			return "", nil, fmt.Errorf("work item %s is %s; delete requires archived state", manifest.ID, manifest.State)
		}
		if err := projection.Delete([]byte(command.ItemID)); err != nil {
			return "", nil, err
		}
		if err := tx.Bucket(bucketImportSources).Delete([]byte(command.ItemID)); err != nil {
			return "", nil, err
		}
		for _, name := range []string{AgentObservationProjection, AttentionActivityProjection, RuntimeActivityProjection, PiSessionProjection, RuntimeOwnershipProjection, TerminalOwnershipProjection} {
			if bucket := tx.Bucket(bucketProjections).Bucket([]byte(name)); bucket != nil {
				_ = bucket.Delete([]byte(command.ItemID))
			}
		}
		return "work_item.deleted", compact, nil
	default:
		return "", nil, fmt.Errorf("unsupported store operation %q", command.Operation)
	}
}

func putProjectionValue(tx *bolt.Tx, projection, key string, value any) error {
	bucket, err := tx.Bucket(bucketProjections).CreateBucketIfNotExists([]byte(projection))
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return bucket.Put([]byte(key), encoded)
}

func ptrTime(value time.Time) *time.Time { return &value }

func sameStoreCommand(left, right StoreCommand) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes)
}

func mutationStagePath(database *Database, commandID string) string {
	digest := sha256.Sum256([]byte("mutation:" + commandID))
	return filepath.Join(filepath.Dir(database.path), ".coordinator-pending", hex.EncodeToString(digest[:])+".mutation")
}

func StageStoreMutation(database *Database, command StoreCommand, payload json.RawMessage) error {
	if len(payload) > MaxRequestBytes {
		return errors.New("store mutation payload is too large")
	}
	if StorePayloadDigest(payload) != command.PayloadDigest {
		return errors.New("store mutation payload digest mismatch")
	}
	path := mutationStagePath(database, command.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mutation-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = tmp.Close(); _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(payload); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func RemoveStagedStoreMutation(database *Database, commandID string) {
	_ = os.Remove(mutationStagePath(database, commandID))
}

func requiresNativeWrite(operation string) bool {
	switch operation {
	case StoreItemDelete:
		return true
	default:
		return false
	}
}
