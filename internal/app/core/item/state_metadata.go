package item

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
)

func (s *Service) DeepWorkCapacity(maxActive int) Capacity {
	if maxActive < 0 {
		maxActive = 0
	}
	capacity := Capacity{Limit: maxActive, ItemIDs: []string{}}
	items, _ := s.store.ListManifests()
	for _, m := range items {
		if m.DeepWork && m.State == model.StateActive {
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
	_ = s.store.AppendEvent(ctx, m.ID, model.NewEventFrom(ctx, m.UpdatedAt, eventType, map[string]any{"deep_work": deep}))
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
		_ = s.store.AppendEvent(ctx, m.ID, model.NewEventFrom(ctx, m.UpdatedAt, "label.added", map[string]any{"label": label}))
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
		_ = s.store.AppendEvent(ctx, m.ID, model.NewEventFrom(ctx, m.UpdatedAt, "label.removed", map[string]any{"label": label}))
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
