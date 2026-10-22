package coordinator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

// Blocker clear causes recorded in blocker.cleared events.
const (
	BlockerClearedManual      = "manual"
	BlockerClearedExpired     = "expired"
	BlockerClearedItemClosed  = "item_closed"
	BlockerClearedItemMissing = "item_missing"
	BlockerClearedClosed      = "closed"
	// BlockerClearedPrompted ends a snooze: prompting the item means the
	// operator decided it is time to work on it.
	BlockerClearedPrompted = "prompted"
)

func blockerID(commandID string, index int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("blocker:%s:%d", commandID, index)))
	return "b" + hex.EncodeToString(digest[:4])
}

func blockerEventData(blocker model.Blocker) map[string]any {
	data := map[string]any{"blocker_id": blocker.ID, "kind": blocker.Kind}
	if blocker.Until != nil {
		data["until"] = blocker.Until.UTC()
	}
	if blocker.ItemID != "" {
		data["blocking_item_id"] = blocker.ItemID
	}
	if blocker.Event != "" {
		data["event"] = blocker.Event
	}
	if blocker.Label != "" {
		data["label"] = blocker.Label
	}
	if len(blocker.Meta) > 0 {
		data["meta"] = blocker.Meta
	}
	return data
}

// applyBlockerAddCommand adds one or more blockers atomically; one invalid
// blocker rejects the whole command.
func applyBlockerAddCommand(command ManifestCommand, manifest *model.Manifest, all []model.Manifest, appendEvent func(string, map[string]any)) error {
	if len(command.Blockers) == 0 {
		return errors.New("at least one blocker is required")
	}
	if manifest.State == model.StateClosed {
		return fmt.Errorf("work item %s is closed; closed items cannot wait", manifest.ID)
	}
	now := command.CreatedAt.UTC()
	byID := map[string]model.Manifest{}
	for _, other := range all {
		byID[other.ID] = other
	}
	added := 0
	for index, requested := range command.Blockers {
		blocker := requested
		blocker.Event = strings.TrimSpace(blocker.Event)
		blocker.Label = strings.TrimSpace(blocker.Label)
		blocker.ID = blockerID(command.ID, index)
		blocker.CreatedAt = now
		if blocker.Until != nil {
			until := blocker.Until.UTC()
			blocker.Until = &until
		}
		if len(blocker.Meta) > 0 {
			meta := make(map[string]string, len(blocker.Meta))
			for key, value := range blocker.Meta {
				meta[key] = value
			}
			blocker.Meta = meta
		} else {
			blocker.Meta = nil
		}
		if err := model.ValidateBlocker(blocker, manifest.ID, now); err != nil {
			return err
		}
		if blocker.Kind == model.BlockerItem {
			target, ok := byID[blocker.ItemID]
			if !ok {
				return fmt.Errorf("work item %s does not exist", blocker.ItemID)
			}
			if target.State == model.StateClosed {
				return fmt.Errorf("work item %s is already closed", blocker.ItemID)
			}
			duplicate := false
			for _, existing := range manifest.Blockers {
				duplicate = duplicate || existing.Kind == model.BlockerItem && existing.ItemID == blocker.ItemID
			}
			if duplicate {
				continue
			}
			if blocksTransitively(byID, blocker.ItemID, manifest.ID) {
				return fmt.Errorf("work item %s already waits, directly or indirectly, on %s; refusing a dependency cycle", blocker.ItemID, manifest.ID)
			}
		}
		manifest.Blockers = append(manifest.Blockers, blocker)
		byID[manifest.ID] = *manifest
		appendEvent("blocker.added", blockerEventData(blocker))
		added++
	}
	if added > 0 {
		// Parking an item is a decision made after seeing it: its current
		// responses must not pull it straight back into the queue.
		appendEvent("attention.read", map[string]any{"at": now, "cause": "waiting"})
	}
	return nil
}

// blocksTransitively reports whether from is blocked, through item blockers, by target.
func blocksTransitively(byID map[string]model.Manifest, from, target string) bool {
	seen := map[string]bool{}
	stack := []string{from}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[current] {
			continue
		}
		seen[current] = true
		for _, blocker := range byID[current].Blockers {
			if blocker.Kind != model.BlockerItem {
				continue
			}
			if blocker.ItemID == target {
				return true
			}
			stack = append(stack, blocker.ItemID)
		}
	}
	return false
}

func applyBlockerClearCommand(command ManifestCommand, manifest *model.Manifest, appendEvent func(string, map[string]any)) error {
	id := strings.TrimSpace(command.BlockerID)
	if id == "" {
		clearAllBlockers(manifest, BlockerClearedManual, appendEvent)
		return nil
	}
	remaining := []model.Blocker{}
	found := false
	for _, blocker := range manifest.Blockers {
		if blocker.ID == id {
			found = true
			data := blockerEventData(blocker)
			data["cause"] = BlockerClearedManual
			appendEvent("blocker.cleared", data)
			continue
		}
		remaining = append(remaining, blocker)
	}
	if !found {
		return fmt.Errorf("work item %s has no blocker %s", manifest.ID, id)
	}
	manifest.Blockers = nilIfEmpty(remaining)
	return nil
}

func clearAllBlockers(manifest *model.Manifest, cause string, appendEvent func(string, map[string]any)) {
	for _, blocker := range manifest.Blockers {
		data := blockerEventData(blocker)
		data["cause"] = cause
		appendEvent("blocker.cleared", data)
	}
	manifest.Blockers = nil
}

func nilIfEmpty(blockers []model.Blocker) []model.Blocker {
	if len(blockers) == 0 {
		return nil
	}
	return blockers
}

// SweepBlockers clears blockers whose condition the coordinator can observe:
// expired time blockers, time blockers followed by a prompt to the item, and
// item blockers whose blocking item closed or no longer exists. Item and
// external blockers are facts about the world and survive prompts. Each clearance is an ordinary item event, so an item's
// history records when and why it unblocked even if the daemon was offline at
// the scheduled time.
func (d *Database) SweepBlockers(now time.Time) (int, error) {
	now = now.UTC()
	cleared := 0
	err := d.db.Update(func(tx *bolt.Tx) error {
		projection := tx.Bucket(bucketProjections).Bucket([]byte(ManifestProjection))
		if projection == nil {
			return nil
		}
		all, err := manifestsFromProjection(projection)
		if err != nil {
			return err
		}
		byID := map[string]model.Manifest{}
		for _, manifest := range all {
			byID[manifest.ID] = manifest
		}
		attention := tx.Bucket(bucketProjections).Bucket([]byte(AttentionActivityProjection))
		for _, manifest := range all {
			if manifest.State == model.StateClosed || len(manifest.Blockers) == 0 {
				continue
			}
			var requested *time.Time
			if attention != nil {
				if encoded := attention.Get([]byte(manifest.ID)); encoded != nil {
					var activity AttentionActivity
					if json.Unmarshal(encoded, &activity) == nil {
						requested = activity.LastRequestedAt
					}
				}
			}
			pending := []commandEvent{}
			remaining := []model.Blocker{}
			for _, blocker := range manifest.Blockers {
				data := blockerEventData(blocker)
				switch {
				case blocker.Kind == model.BlockerTime && blocker.Until != nil && !blocker.Until.After(now):
					data["cause"] = BlockerClearedExpired
				case blocker.Kind == model.BlockerTime && requested != nil && requested.After(blocker.CreatedAt):
					data["cause"] = BlockerClearedPrompted
					data["prompted_at"] = requested.UTC()
				case blocker.Kind == model.BlockerItem:
					target, ok := byID[blocker.ItemID]
					if !ok {
						data["cause"] = BlockerClearedItemMissing
					} else if target.State == model.StateClosed {
						data["cause"] = BlockerClearedItemClosed
						data["blocking_item_resolution"] = target.Resolution
					} else {
						remaining = append(remaining, blocker)
						continue
					}
				default:
					remaining = append(remaining, blocker)
					continue
				}
				payload, _ := json.Marshal(data)
				pending = append(pending, commandEvent{Type: "blocker.cleared", Payload: payload})
			}
			if len(pending) == 0 {
				continue
			}
			manifest.Blockers = nilIfEmpty(remaining)
			manifest.UpdatedAt = now
			// The global sequence makes repeated sweeps at one instant distinct.
			sequence := decodeUint64(tx.Bucket(bucketMeta).Get(keyGlobalSequence))
			causation := fmt.Sprintf("blocker-sweep-%s-%d-%d", manifest.ID, now.UnixNano(), sequence)
			if _, _, err := commitManifestEvents(tx, manifest, causation, "wi", now, pending); err != nil {
				return err
			}
			cleared += len(pending)
		}
		return nil
	})
	if err == nil && cleared > 0 {
		d.changes.recordAll()
	}
	return cleared, err
}

// commitManifestEvents appends compact domain events for one item, advances
// its revision, and stores the updated manifest projection in tx.
func commitManifestEvents(tx *bolt.Tx, manifest model.Manifest, causationID, actor string, createdAt time.Time, pending []commandEvent) ([]DomainEvent, uint64, error) {
	meta, events, eventIDs, revisions := tx.Bucket(bucketMeta), tx.Bucket(bucketEvents), tx.Bucket(bucketEventIDs), tx.Bucket(bucketRevisions)
	projection := tx.Bucket(bucketProjections).Bucket([]byte(ManifestProjection))
	sequence := decodeUint64(meta.Get(keyGlobalSequence))
	revision := decodeUint64(revisions.Get([]byte(manifest.ID)))
	committed := []DomainEvent{}
	for index, item := range pending {
		sequence++
		revision++
		eventID := domainEventID(causationID, index)
		if eventIDs.Get([]byte(eventID)) != nil {
			return nil, 0, fmt.Errorf("duplicate event id %s", eventID)
		}
		event := DomainEvent{Sequence: sequence, ID: eventID, ItemID: manifest.ID, ItemRevision: revision, Type: item.Type, Timestamp: createdAt.UTC(), Actor: actor, CausationID: causationID, Payload: item.Payload}
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, 0, err
		}
		if err := events.Put(encodeUint64(sequence), encoded); err != nil {
			return nil, 0, err
		}
		if err := eventIDs.Put([]byte(eventID), encodeUint64(sequence)); err != nil {
			return nil, 0, err
		}
		committed = append(committed, event)
	}
	if err := revisions.Put([]byte(manifest.ID), encodeUint64(revision)); err != nil {
		return nil, 0, err
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return nil, 0, err
	}
	if err := projection.Put([]byte(manifest.ID), manifestBytes); err != nil {
		return nil, 0, err
	}
	digest := sha256.Sum256(manifestBytes)
	if err := tx.Bucket(bucketImportSources).Put([]byte(manifest.ID), []byte(hex.EncodeToString(digest[:]))); err != nil {
		return nil, 0, err
	}
	if err := meta.Put(keyGlobalSequence, encodeUint64(sequence)); err != nil {
		return nil, 0, err
	}
	for _, event := range committed {
		if event.Type != "attention.read" {
			continue
		}
		if err := recordAttentionMarkerTx(tx, manifest.ID, event.Type, event.Timestamp); err != nil {
			return nil, 0, err
		}
	}
	return committed, revision, nil
}
