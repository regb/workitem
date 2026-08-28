package coordinator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/priority"
	"github.com/regb/workitem/internal/store"
	bolt "go.etcd.io/bbolt"
)

const (
	CommandLabelsAdd    = "item.labels.add"
	CommandLabelsRemove = "item.labels.remove"
	CommandDeepWorkSet  = "item.deep_work.set"
	CommandStateSet     = "item.state.set"
)

type ManifestCommand struct {
	ID               string    `json:"id"`
	ProtocolVersion  int       `json:"protocol_version"`
	Type             string    `json:"type"`
	ItemID           string    `json:"item_id"`
	ExpectedRevision *uint64   `json:"expected_revision"`
	Actor            string    `json:"actor"`
	Labels           []string  `json:"labels,omitempty"`
	DeepWork         *bool     `json:"deep_work,omitempty"`
	TargetState      string    `json:"target_state,omitempty"`
	EventType        string    `json:"event_type,omitempty"`
	Force            bool      `json:"force,omitempty"`
	MaxActive        int       `json:"max_active,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

type CanonicalManifest struct {
	Manifest model.Manifest `json:"manifest"`
	Revision uint64         `json:"revision"`
}

type ManifestCommandResult struct {
	CommandID   string         `json:"command_id"`
	Manifest    model.Manifest `json:"manifest"`
	Revision    uint64         `json:"revision"`
	Changed     bool           `json:"changed"`
	Events      []DomainEvent  `json:"events"`
	Duplicate   bool           `json:"duplicate,omitempty"`
	CommittedAt time.Time      `json:"committed_at"`
}

type ActionabilityQueueOptions struct {
	Strategy   string          `json:"strategy"`
	LabelRules map[string]bool `json:"label_rules,omitempty"`
}

type ActionabilityQueueCandidate struct {
	Manifest model.Manifest    `json:"manifest"`
	Activity AttentionActivity `json:"activity"`
	Rank     int               `json:"rank"`
}

type ActionabilityQueueResult struct {
	Strategy   string                        `json:"strategy"`
	Candidates []ActionabilityQueueCandidate `json:"candidates"`
}

type ActionabilitySelection struct {
	Found           bool   `json:"found"`
	Index           int    `json:"index"`
	WorkItemID      string `json:"work_item_id,omitempty"`
	CurrentInQueue  bool   `json:"current_in_queue"`
	Wrapped         bool   `json:"wrapped"`
	TerminalSession string `json:"terminal_session,omitempty"`
}

type ManifestCommandQueueRequest struct {
	Command ManifestCommand           `json:"command"`
	Queue   ActionabilityQueueOptions `json:"queue"`
}

type ManifestCommandQueueResult struct {
	Command   ManifestCommandResult    `json:"command"`
	Queue     ActionabilityQueueResult `json:"queue"`
	Selection ActionabilitySelection   `json:"selection"`
}

type domainCommandOutcome struct {
	Command      ManifestCommand            `json:"command"`
	Result       ManifestCommandResult      `json:"result"`
	QueueOptions *ActionabilityQueueOptions `json:"queue_options,omitempty"`
	Queue        *ActionabilityQueueResult  `json:"queue,omitempty"`
}

type pendingNativeWrite struct {
	CommandID string         `json:"command_id"`
	Sequence  uint64         `json:"sequence"`
	Operation string         `json:"operation,omitempty"`
	Manifest  model.Manifest `json:"manifest"`
}

func (d *Database) CanonicalManifest(itemID string) (CanonicalManifest, error) {
	if !model.ValidID(itemID) {
		return CanonicalManifest{}, fmt.Errorf("invalid work item id %q", itemID)
	}
	var result CanonicalManifest
	err := d.db.View(func(tx *bolt.Tx) error {
		projection := tx.Bucket(bucketProjections).Bucket([]byte(ManifestProjection))
		if projection == nil || projection.Get([]byte(itemID)) == nil {
			return store.ErrNotFound
		}
		if err := json.Unmarshal(projection.Get([]byte(itemID)), &result.Manifest); err != nil {
			return err
		}
		result.Revision = decodeUint64(tx.Bucket(bucketRevisions).Get([]byte(itemID)))
		return nil
	})
	return result, err
}

func (d *Database) ExecuteManifestCommand(command ManifestCommand) (ManifestCommandResult, error) {
	result, _, err := d.executeManifestCommand(command, nil)
	return result, err
}

func (d *Database) ExecuteManifestCommandWithQueue(command ManifestCommand, options ActionabilityQueueOptions) (ManifestCommandQueueResult, error) {
	result, queue, err := d.executeManifestCommand(command, &options)
	return ManifestCommandQueueResult{Command: result, Queue: queue, Selection: SelectActionability(queue, command.ItemID)}, err
}

func (d *Database) executeManifestCommand(command ManifestCommand, queueOptions *ActionabilityQueueOptions) (ManifestCommandResult, ActionabilityQueueResult, error) {
	if command.ID == "" || !model.ValidID(command.ItemID) || command.ExpectedRevision == nil {
		return ManifestCommandResult{}, ActionabilityQueueResult{}, errors.New("command id, item id, and expected revision are required")
	}
	if compactIdentifier(command.ID, 200) != command.ID {
		return ManifestCommandResult{}, ActionabilityQueueResult{}, errors.New("command id is invalid")
	}
	if command.ProtocolVersion != ProtocolVersion {
		return ManifestCommandResult{}, ActionabilityQueueResult{}, fmt.Errorf("unsupported command protocol %d", command.ProtocolVersion)
	}
	if command.Actor == "" {
		command.Actor = "user"
	}
	if command.Actor != "user" {
		return ManifestCommandResult{}, ActionabilityQueueResult{}, errors.New("manifest commands require user actor")
	}
	if command.CreatedAt.IsZero() {
		command.CreatedAt = time.Now().UTC()
	}
	var result ManifestCommandResult
	var queue ActionabilityQueueResult
	err := d.db.Update(func(tx *bolt.Tx) error {
		outcomes := tx.Bucket(bucketDomainCommands)
		if encoded := outcomes.Get([]byte(command.ID)); encoded != nil {
			var outcome domainCommandOutcome
			if err := json.Unmarshal(encoded, &outcome); err != nil {
				return err
			}
			if !sameManifestCommand(outcome.Command, command) {
				return fmt.Errorf("command id %s was already used with different input", command.ID)
			}
			if queueOptions == nil && outcome.QueueOptions != nil {
				return fmt.Errorf("command id %s was already used with queue input", command.ID)
			}
			result = outcome.Result
			result.Duplicate = true
			if queueOptions != nil {
				if outcome.QueueOptions == nil || !sameActionabilityQueueOptions(*outcome.QueueOptions, *queueOptions) {
					return fmt.Errorf("command id %s was already used with different queue input", command.ID)
				}
				if outcome.Queue == nil {
					return fmt.Errorf("command id %s has no stored actionability queue", command.ID)
				}
				queue = *outcome.Queue
			}
			return nil
		}
		projection, err := tx.Bucket(bucketProjections).CreateBucketIfNotExists([]byte(ManifestProjection))
		if err != nil {
			return err
		}
		encodedManifest := projection.Get([]byte(command.ItemID))
		if encodedManifest == nil {
			return store.ErrNotFound
		}
		var manifest model.Manifest
		if err := json.Unmarshal(encodedManifest, &manifest); err != nil {
			return err
		}
		revisions := tx.Bucket(bucketRevisions)
		revision := decodeUint64(revisions.Get([]byte(command.ItemID)))
		if revision != *command.ExpectedRevision {
			return fmt.Errorf("item revision conflict: expected %d, current %d", *command.ExpectedRevision, revision)
		}
		allManifests, err := manifestsFromProjection(projection)
		if err != nil {
			return err
		}
		pendingEvents, changed, err := applyManifestCommand(command, &manifest, allManifests)
		if err != nil {
			return err
		}
		result = ManifestCommandResult{CommandID: command.ID, Manifest: manifest, Revision: revision, Changed: changed, Events: []DomainEvent{}, CommittedAt: time.Now().UTC()}
		if changed {
			meta, events, eventIDs := tx.Bucket(bucketMeta), tx.Bucket(bucketEvents), tx.Bucket(bucketEventIDs)
			sequence := decodeUint64(meta.Get(keyGlobalSequence))
			for index, pending := range pendingEvents {
				sequence++
				revision++
				eventID := domainEventID(command.ID, index)
				if eventIDs.Get([]byte(eventID)) != nil {
					return fmt.Errorf("duplicate event id %s", eventID)
				}
				event := DomainEvent{Sequence: sequence, ID: eventID, ItemID: command.ItemID, ItemRevision: revision, Type: pending.Type, Timestamp: command.CreatedAt.UTC(), Actor: command.Actor, CausationID: command.ID, Payload: pending.Payload}
				encoded, err := json.Marshal(event)
				if err != nil {
					return err
				}
				if err := events.Put(encodeUint64(sequence), encoded); err != nil {
					return err
				}
				if err := eventIDs.Put([]byte(eventID), encodeUint64(sequence)); err != nil {
					return err
				}
				result.Events = append(result.Events, event)
			}
			if err := revisions.Put([]byte(command.ItemID), encodeUint64(revision)); err != nil {
				return err
			}
			manifestBytes, err := json.Marshal(manifest)
			if err != nil {
				return err
			}
			if err := projection.Put([]byte(command.ItemID), manifestBytes); err != nil {
				return err
			}
			digest := sha256.Sum256(manifestBytes)
			if err := tx.Bucket(bucketImportSources).Put([]byte(command.ItemID), []byte(hex.EncodeToString(digest[:]))); err != nil {
				return err
			}
			if err := meta.Put(keyGlobalSequence, encodeUint64(sequence)); err != nil {
				return err
			}
			result.Manifest = manifest
			result.Revision = revision
		}
		if queueOptions != nil {
			queue, err = actionabilityQueueFromTransaction(tx, *queueOptions)
			if err != nil {
				return err
			}
		}
		stored := result
		stored.Duplicate = false
		var storedQueue *ActionabilityQueueResult
		var storedQueueOptions *ActionabilityQueueOptions
		if queueOptions != nil {
			copy := queue
			storedQueue = &copy
			optionsCopy := *queueOptions
			storedQueueOptions = &optionsCopy
		}
		encodedResult, err := json.Marshal(domainCommandOutcome{Command: command, Result: stored, QueueOptions: storedQueueOptions, Queue: storedQueue})
		if err != nil {
			return err
		}
		return outcomes.Put([]byte(command.ID), encodedResult)
	})
	if err != nil {
		return ManifestCommandResult{}, ActionabilityQueueResult{}, err
	}
	return result, queue, nil
}

type commandEvent struct {
	Type    string
	Payload json.RawMessage
}

func applyManifestCommand(command ManifestCommand, manifest *model.Manifest, allManifests []model.Manifest) ([]commandEvent, bool, error) {
	now := command.CreatedAt.UTC()
	pending := []commandEvent{}
	appendEvent := func(eventType string, data map[string]any) {
		payload, _ := json.Marshal(data)
		pending = append(pending, commandEvent{Type: eventType, Payload: payload})
	}
	switch command.Type {
	case CommandLabelsAdd:
		updated, changed, err := model.AddLabels(manifest.Labels, command.Labels...)
		if err != nil {
			return nil, false, err
		}
		if len(changed) == 0 {
			return pending, false, nil
		}
		manifest.Labels = updated
		for _, label := range changed {
			appendEvent("label.added", map[string]any{"label": label})
		}
	case CommandLabelsRemove:
		updated, changed, err := model.RemoveLabels(manifest.Labels, command.Labels...)
		if err != nil {
			return nil, false, err
		}
		if len(changed) == 0 {
			return pending, false, nil
		}
		manifest.Labels = updated
		for _, label := range changed {
			appendEvent("label.removed", map[string]any{"label": label})
		}
	case CommandDeepWorkSet:
		if command.DeepWork == nil {
			return nil, false, errors.New("deep_work is required")
		}
		if manifest.DeepWork == *command.DeepWork {
			return pending, false, nil
		}
		manifest.DeepWork = *command.DeepWork
		eventType := "deep_work.cleared"
		if *command.DeepWork {
			eventType = "deep_work.set"
		}
		appendEvent(eventType, map[string]any{"deep_work": *command.DeepWork})
	case CommandStateSet:
		if err := applyStateSetCommand(command, manifest, allManifests, appendEvent); err != nil {
			return nil, false, err
		}
		if len(pending) == 0 {
			return pending, false, nil
		}
		manifest.StateChangedAt = now
	default:
		return nil, false, fmt.Errorf("unsupported manifest command %q", command.Type)
	}
	manifest.UpdatedAt = now
	return pending, true, nil
}

func manifestsFromProjection(bucket *bolt.Bucket) ([]model.Manifest, error) {
	manifests := []model.Manifest{}
	err := bucket.ForEach(func(_, value []byte) error {
		var manifest model.Manifest
		if err := json.Unmarshal(value, &manifest); err != nil {
			return err
		}
		manifests = append(manifests, manifest)
		return nil
	})
	return manifests, err
}

func (d *Database) ActionabilityQueue(options ActionabilityQueueOptions) (ActionabilityQueueResult, error) {
	var result ActionabilityQueueResult
	err := d.db.View(func(tx *bolt.Tx) error {
		var err error
		result, err = actionabilityQueueFromTransaction(tx, options)
		return err
	})
	return result, err
}

func actionabilityQueueFromTransaction(tx *bolt.Tx, options ActionabilityQueueOptions) (ActionabilityQueueResult, error) {
	projection := tx.Bucket(bucketProjections)
	manifestsBucket := projection.Bucket([]byte(ManifestProjection))
	observationsBucket := projection.Bucket([]byte(AgentObservationProjection))
	if manifestsBucket == nil {
		return ActionabilityQueueResult{}, errors.New("manifest projection is unavailable")
	}
	manifests, err := manifestsFromProjection(manifestsBucket)
	if err != nil {
		return ActionabilityQueueResult{}, err
	}
	sort.SliceStable(manifests, func(i, j int) bool {
		if manifests[i].DeepWork != manifests[j].DeepWork {
			return manifests[i].DeepWork
		}
		if !manifests[i].StateChangedAt.Equal(manifests[j].StateChangedAt) {
			return manifests[i].StateChangedAt.Before(manifests[j].StateChangedAt)
		}
		return manifests[i].ID < manifests[j].ID
	})
	projected := map[string]ActionabilityQueueCandidate{}
	candidates := []priority.Candidate{}
	for _, manifest := range manifests {
		if manifest.State != model.StateWorking || !matchesCanonicalLabels(manifest.Labels, options.LabelRules) || observationsBucket == nil {
			continue
		}
		encoded := observationsBucket.Get([]byte(manifest.ID))
		if encoded == nil {
			continue
		}
		var observation AgentObservation
		if err := json.Unmarshal(encoded, &observation); err != nil {
			return ActionabilityQueueResult{}, err
		}
		if observation.Status == "" || observation.Status == "busy" || observation.Status == "problem" || observation.Worktree != nil && observation.Worktree.Status == "problem" {
			continue
		}
		baseOrder := len(candidates)
		candidate := priority.Candidate{ID: manifest.ID, DeepWork: manifest.DeepWork, BaseOrder: baseOrder, StateChangedAt: manifest.StateChangedAt, LastRequestedAt: observation.Activity.LastRequestedAt, LastCompletedAt: observation.Activity.LastCompletedAt, LastDeferredAt: observation.Activity.LastDeferredAt}
		candidates = append(candidates, candidate)
		projected[manifest.ID] = ActionabilityQueueCandidate{Manifest: manifest, Activity: observation.Activity}
	}
	ranked, err := priority.Rank(options.Strategy, candidates)
	if err != nil {
		return ActionabilityQueueResult{}, err
	}
	strategy := strings.TrimSpace(options.Strategy)
	if strategy == "" {
		strategy = priority.DefaultStrategy
	}
	result := ActionabilityQueueResult{Strategy: strategy, Candidates: make([]ActionabilityQueueCandidate, 0, len(ranked))}
	for index, candidate := range ranked {
		value := projected[candidate.ID]
		value.Rank = index + 1
		result.Candidates = append(result.Candidates, value)
	}
	return result, nil
}

func SelectActionability(queue ActionabilityQueueResult, currentItemID string) ActionabilitySelection {
	if len(queue.Candidates) == 0 {
		return ActionabilitySelection{}
	}
	index, currentInQueue := 0, false
	for candidateIndex, candidate := range queue.Candidates {
		if candidate.Manifest.ID == currentItemID {
			index = (candidateIndex + 1) % len(queue.Candidates)
			currentInQueue = true
			break
		}
	}
	candidate := queue.Candidates[index]
	return ActionabilitySelection{Found: true, Index: index, WorkItemID: candidate.Manifest.ID, CurrentInQueue: currentInQueue, Wrapped: currentInQueue && index == 0, TerminalSession: candidate.Manifest.TerminalSessionName()}
}

func matchesCanonicalLabels(labels []string, rules map[string]bool) bool {
	for label, include := range rules {
		if model.HasLabel(labels, label) != include {
			return false
		}
	}
	return true
}

func applyStateSetCommand(command ManifestCommand, manifest *model.Manifest, all []model.Manifest, appendEvent func(string, map[string]any)) error {
	target := command.TargetState
	if target != model.StateBacklog && target != model.StateWorking && target != model.StateWaiting && target != model.StateArchived {
		return fmt.Errorf("invalid state %q; expected backlog, working, waiting, or archived", target)
	}
	previous := manifest.State
	if previous == target {
		return nil
	}
	if previous == model.StateArchived {
		if target != model.StateBacklog {
			return fmt.Errorf("work item %s is archived; restore it to backlog before changing it", manifest.ID)
		}
		slug, err := uniqueCanonicalSlug(model.Slugify(manifest.Title), manifest.ID, all)
		if err != nil {
			return err
		}
		manifest.Slug, manifest.State = slug, model.StateBacklog
		appendEvent("work_item.state_set", map[string]any{"previous_state": previous, "new_state": manifest.State, "slug": slug, "workspace_unchanged": true})
		return nil
	}
	if target == model.StateArchived {
		previousSlug := manifest.Slug
		manifest.State, manifest.Slug = model.StateArchived, ""
		appendEvent("work_item.state_set", map[string]any{"previous_state": previous, "new_state": manifest.State, "previous_slug": previousSlug, "workspace_unchanged": true})
		return nil
	}
	if previous == model.StateBacklog && target != model.StateWorking {
		return fmt.Errorf("cannot move backlog item directly to %s; run `wi start` first", target)
	}
	valid := target == model.StateWorking && (previous == model.StateBacklog || previous == model.StateWaiting) ||
		target == model.StateWaiting && previous == model.StateWorking ||
		target == model.StateBacklog && (previous == model.StateWorking || previous == model.StateWaiting)
	if !valid {
		return fmt.Errorf("invalid state transition %s -> %s", previous, target)
	}
	active, activeIDs := canonicalDeepWorkCapacity(all)
	if command.MaxActive < 0 {
		command.MaxActive = 0
	}
	if manifest.DeepWork && previous == model.StateBacklog && target == model.StateWorking && active >= command.MaxActive && !command.Force {
		return fmt.Errorf("cannot activate %q. Deep work active limit reached: %d/%d. Currently active: %s. Use --force to activate this work item anyway", manifest.Title, active, command.MaxActive, strings.Join(activeIDs, ", "))
	}
	manifest.State = target
	eventType := command.EventType
	if eventType == "" {
		eventType = "work_item.state_set"
	}
	if eventType != "work_item.state_set" && eventType != "work_item.started" && eventType != "work_item.resumed" {
		return fmt.Errorf("unsupported state event type %q", eventType)
	}
	data := map[string]any{"previous_state": previous, "new_state": target, "deep_work": manifest.DeepWork, "active_before": active, "limit": command.MaxActive, "forced": command.Force}
	if eventType == "work_item.state_set" {
		data["workspace_unchanged"] = true
	}
	if command.Force && manifest.DeepWork && previous == model.StateBacklog {
		appendEvent("deep_work_limit.overridden", data)
	}
	appendEvent(eventType, data)
	return nil
}

func canonicalDeepWorkCapacity(manifests []model.Manifest) (int, []string) {
	active, ids := 0, []string{}
	for _, manifest := range manifests {
		if manifest.DeepWork && (manifest.State == model.StateWorking || manifest.State == model.StateWaiting) {
			active++
			ids = append(ids, manifest.ID)
		}
	}
	sort.Strings(ids)
	return active, ids
}

func uniqueCanonicalSlug(base, itemID string, manifests []model.Manifest) (string, error) {
	used := map[string]bool{}
	for _, manifest := range manifests {
		if manifest.ID != itemID && manifest.State != model.StateArchived && manifest.Slug != "" {
			used[manifest.Slug] = true
		}
	}
	if base == "" {
		base = "item"
	}
	if !used[base] {
		return base, nil
	}
	for index := 2; index <= 10000; index++ {
		suffix := fmt.Sprintf("-%d", index)
		limit := model.MaxSlugLength - len(suffix)
		candidate := strings.Trim(base[:min(len(base), limit)], "-") + suffix
		if !used[candidate] {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not allocate a unique slug for %q", base)
}

func sameActionabilityQueueOptions(left, right ActionabilityQueueOptions) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes)
}

func sameManifestCommand(left, right ManifestCommand) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes)
}

func domainEventID(commandID string, index int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", commandID, index)))
	return "event-" + hex.EncodeToString(digest[:16])
}

func (d *Database) PendingNativeWrites() ([]pendingNativeWrite, error) {
	pending := []pendingNativeWrite{}
	err := d.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketPendingWrites).ForEach(func(_, value []byte) error {
			var write pendingNativeWrite
			if err := json.Unmarshal(value, &write); err != nil {
				return err
			}
			pending = append(pending, write)
			return nil
		})
	})
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].Sequence == pending[j].Sequence {
			return pending[i].CommandID < pending[j].CommandID
		}
		return pending[i].Sequence < pending[j].Sequence
	})
	return pending, err
}

func (d *Database) ClearPendingNativeWrite(commandID string) error {
	return d.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketPendingWrites).Delete([]byte(commandID)) })
}
