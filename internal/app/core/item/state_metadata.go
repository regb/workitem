package item

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
)

func (s *Service) State(ctx context.Context, opts contract.ResolveOptions) (StateResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return StateResult{}, err
	}
	return StateResult{WorkItemID: m.ID, State: m.State, Manifest: m}, nil
}

func (s *Service) SetState(ctx context.Context, opts contract.ResolveOptions, target string, force bool, maxActive int) (StateTransitionResult, error) {
	target = strings.TrimSpace(target)
	switch target {
	case model.StateWorking, model.StateWaiting:
		return s.TransitionState(ctx, opts, target, "work_item.state_set", force, maxActive)
	case model.StateBacklog:
		m, err := s.resolve(ctx, opts)
		if err != nil {
			return StateTransitionResult{}, err
		}
		if m.State == model.StateArchived {
			return s.restoreArchived(ctx, m)
		}
		return s.TransitionState(ctx, opts, target, "work_item.state_set", force, maxActive)
	case model.StateArchived:
		return s.archiveState(ctx, opts)
	default:
		return StateTransitionResult{}, fmt.Errorf("invalid state %q; expected backlog, working, waiting, or archived", target)
	}
}

func (s *Service) restoreArchived(ctx context.Context, m model.Manifest) (StateTransitionResult, error) {
	previous := m.State
	if m.State != model.StateArchived {
		return StateTransitionResult{WorkItemID: m.ID, PreviousState: previous, State: m.State, Manifest: m}, nil
	}
	slug, err := s.UniqueSlug(model.Slugify(m.Title))
	if err != nil {
		return StateTransitionResult{}, err
	}
	now := s.now()
	m.Slug, m.State, m.StateChangedAt, m.UpdatedAt = slug, model.StateBacklog, now, now
	if err := s.store.SaveManifest(ctx, m); err != nil {
		return StateTransitionResult{}, err
	}
	_ = s.store.AppendEvent(ctx, m.ID, model.NewEvent(now, "work_item.state_set", "user", map[string]any{"previous_state": previous, "new_state": m.State, "slug": slug, "workspace_unchanged": true}))
	return StateTransitionResult{WorkItemID: m.ID, PreviousState: previous, State: m.State, Changed: true, Manifest: m}, nil
}

func (s *Service) archiveState(ctx context.Context, opts contract.ResolveOptions) (StateTransitionResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return StateTransitionResult{}, err
	}
	previous := m.State
	if previous == model.StateArchived {
		return StateTransitionResult{WorkItemID: m.ID, PreviousState: previous, State: m.State, Manifest: m}, nil
	}
	now, previousSlug := s.now(), m.Slug
	m.State, m.Slug, m.StateChangedAt, m.UpdatedAt = model.StateArchived, "", now, now
	if err := s.store.SaveManifest(ctx, m); err != nil {
		return StateTransitionResult{}, err
	}
	_ = s.store.AppendEvent(ctx, m.ID, model.NewEvent(now, "work_item.state_set", "user", map[string]any{"previous_state": previous, "new_state": m.State, "previous_slug": previousSlug, "workspace_unchanged": true}))
	return StateTransitionResult{WorkItemID: m.ID, PreviousState: previous, State: m.State, Changed: true, Manifest: m}, nil
}

func (s *Service) TransitionState(ctx context.Context, opts contract.ResolveOptions, target, eventType string, force bool, maxActive int) (StateTransitionResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return StateTransitionResult{}, err
	}
	if m.State == model.StateArchived {
		return StateTransitionResult{}, fmt.Errorf("work item %s is archived; run `wi state set backlog --item %s` before changing it", m.ID, m.ID)
	}
	previous := m.State
	capacity := s.DeepWorkCapacity(maxActive)
	if previous == target {
		return StateTransitionResult{WorkItemID: m.ID, PreviousState: previous, State: m.State, Manifest: m, DeepWork: m.DeepWork, Capacity: &capacity}, nil
	}
	if previous == model.StateBacklog && target != model.StateWorking {
		return StateTransitionResult{}, fmt.Errorf("cannot move backlog item directly to %s; run `wi start` first", target)
	}
	valid := target == model.StateWorking && (previous == model.StateBacklog || previous == model.StateWaiting) ||
		target == model.StateWaiting && previous == model.StateWorking ||
		target == model.StateBacklog && (previous == model.StateWorking || previous == model.StateWaiting)
	if !valid {
		return StateTransitionResult{}, fmt.Errorf("invalid state transition %s -> %s", previous, target)
	}
	if m.DeepWork && previous == model.StateBacklog && target == model.StateWorking && capacity.Active >= capacity.Limit && !force {
		return StateTransitionResult{}, capacityError(m, capacity)
	}
	now := s.now()
	m.State, m.StateChangedAt, m.UpdatedAt = target, now, now
	if err := s.store.SaveManifest(ctx, m); err != nil {
		return StateTransitionResult{}, err
	}
	data := map[string]any{"previous_state": previous, "new_state": target, "deep_work": m.DeepWork, "active_before": capacity.Active, "limit": capacity.Limit, "forced": force}
	if eventType == "work_item.state_set" {
		data["workspace_unchanged"] = true
	}
	if force && m.DeepWork && previous == model.StateBacklog {
		_ = s.store.AppendEvent(ctx, m.ID, model.NewEvent(now, "deep_work_limit.overridden", "user", data))
	}
	_ = s.store.AppendEvent(ctx, m.ID, model.NewEvent(now, eventType, "user", data))
	return StateTransitionResult{WorkItemID: m.ID, PreviousState: previous, State: m.State, Changed: true, Manifest: m, DeepWork: m.DeepWork, Capacity: &capacity}, nil
}

func capacityError(m model.Manifest, capacity Capacity) error {
	return fmt.Errorf("cannot activate %q. Deep work active limit reached: %d/%d. Currently active: %s. Use --force to activate this work item anyway", m.Title, capacity.Active, capacity.Limit, strings.Join(capacity.ItemIDs, ", "))
}

func (s *Service) DeepWorkCapacity(maxActive int) Capacity {
	if maxActive < 0 {
		maxActive = 0
	}
	capacity := Capacity{Limit: maxActive, ItemIDs: []string{}}
	items, _ := s.store.ListManifests()
	for _, m := range items {
		if m.DeepWork && (m.State == model.StateWorking || m.State == model.StateWaiting) {
			capacity.Active++
			capacity.ItemIDs = append(capacity.ItemIDs, m.ID)
		}
	}
	capacity.Available = max(0, capacity.Limit-capacity.Active)
	return capacity
}

func (s *Service) SetDeepWork(ctx context.Context, opts contract.ResolveOptions, deep bool, maxActive int) (DeepWorkResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return DeepWorkResult{}, err
	}
	capacity := s.DeepWorkCapacity(maxActive)
	if m.DeepWork == deep {
		return DeepWorkResult{WorkItemID: m.ID, DeepWork: m.DeepWork, Capacity: &capacity, Manifest: m}, nil
	}
	m.DeepWork, m.UpdatedAt = deep, s.now()
	if err := s.store.SaveManifest(ctx, m); err != nil {
		return DeepWorkResult{}, err
	}
	eventType := "deep_work.cleared"
	if deep {
		eventType = "deep_work.set"
	}
	_ = s.store.AppendEvent(ctx, m.ID, model.NewEvent(m.UpdatedAt, eventType, "user", map[string]any{"deep_work": deep}))
	capacity = s.DeepWorkCapacity(maxActive)
	return DeepWorkResult{WorkItemID: m.ID, DeepWork: m.DeepWork, Changed: true, Capacity: &capacity, Manifest: m}, nil
}

func (s *Service) AddLabels(ctx context.Context, opts contract.ResolveOptions, labels []string) (LabelResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return LabelResult{}, err
	}
	updated, changed, err := model.AddLabels(m.Labels, labels...)
	if err != nil {
		return LabelResult{}, err
	}
	if len(changed) == 0 {
		return LabelResult{WorkItemID: m.ID, Labels: m.Labels, Manifest: m}, nil
	}
	m.Labels, m.UpdatedAt = updated, s.now()
	if err := s.store.SaveManifest(ctx, m); err != nil {
		return LabelResult{}, err
	}
	for _, label := range changed {
		_ = s.store.AppendEvent(ctx, m.ID, model.NewEvent(m.UpdatedAt, "label.added", "user", map[string]any{"label": label}))
	}
	return LabelResult{WorkItemID: m.ID, Labels: m.Labels, Changed: changed, Manifest: m}, nil
}

func (s *Service) RemoveLabels(ctx context.Context, opts contract.ResolveOptions, labels []string) (LabelResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return LabelResult{}, err
	}
	updated, changed, err := model.RemoveLabels(m.Labels, labels...)
	if err != nil {
		return LabelResult{}, err
	}
	if len(changed) == 0 {
		return LabelResult{WorkItemID: m.ID, Labels: m.Labels, Manifest: m}, nil
	}
	m.Labels, m.UpdatedAt = updated, s.now()
	if err := s.store.SaveManifest(ctx, m); err != nil {
		return LabelResult{}, err
	}
	for _, label := range changed {
		_ = s.store.AppendEvent(ctx, m.ID, model.NewEvent(m.UpdatedAt, "label.removed", "user", map[string]any{"label": label}))
	}
	return LabelResult{WorkItemID: m.ID, Labels: m.Labels, Changed: changed, Manifest: m}, nil
}

func (s *Service) ListLabels(ctx context.Context, opts contract.ResolveOptions) (LabelResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return LabelResult{}, err
	}
	return LabelResult{WorkItemID: m.ID, Labels: append([]string{}, m.Labels...), Manifest: m}, nil
}

func (s *Service) Events(ctx context.Context, opts contract.ResolveOptions) (EventsResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return EventsResult{}, err
	}
	events, err := s.store.ReadEvents(m.ID)
	if err != nil {
		return EventsResult{}, err
	}
	return EventsResult{WorkItemID: m.ID, Events: events}, nil
}

func (s *Service) AbsPath(itemID, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("item path %q must be relative", rel)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("item path %q escapes the work-item directory", rel)
	}
	itemDir := s.store.ItemDir(itemID)
	absolute := filepath.Join(itemDir, clean)
	relative, err := filepath.Rel(itemDir, absolute)
	if err != nil {
		return "", err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("item path %q escapes the work-item directory", rel)
	}
	return absolute, nil
}
