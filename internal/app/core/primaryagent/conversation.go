package primaryagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	itemlock "github.com/regb/workitem/internal/lock"
	"github.com/regb/workitem/internal/model"
)

type CreateConversationResult struct {
	Session  model.PiSession
	Manifest model.Manifest
}

func (s *Service) CreateConversation(ctx context.Context, m model.Manifest) (CreateConversationResult, error) {
	if !m.Checkout.Present() || m.Checkout.Path == nil || *m.Checkout.Path == "" {
		return CreateConversationResult{}, fmt.Errorf("checkout is absent; run `wi workspace ensure` first")
	}
	id, err := s.newID()
	if err != nil {
		return CreateConversationResult{}, err
	}
	rel := filepath.Join("sessions", "pi", id+".jsonl")
	absolute, err := s.AbsPath(m.ID, rel)
	if err != nil {
		return CreateConversationResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return CreateConversationResult{}, fmt.Errorf("create Pi session directory: %w", err)
	}
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return CreateConversationResult{}, fmt.Errorf("create Pi session file: %w", err)
	}
	if err := file.Close(); err != nil {
		return CreateConversationResult{}, fmt.Errorf("close Pi session file: %w", err)
	}
	now := s.now()
	session := model.PiSession{ID: id, Path: filepath.ToSlash(rel)}
	m.RootPiSession = &session
	m.UpdatedAt = now
	if err := s.store.SaveManifest(ctx, m); err != nil {
		_ = os.Remove(absolute)
		return CreateConversationResult{}, err
	}
	_ = s.store.AppendEvent(ctx, m.ID, model.NewEvent(now, "pi_session.created", "wi", map[string]any{"session_id": session.ID, "path": session.Path, "root": true}))
	return CreateConversationResult{session, m}, nil
}
func (s *Service) EnsureConversationUnlocked(m model.Manifest, session model.PiSession) error {
	lock, err := itemlock.TryAcquire(s.LockPath(m.ID, session.ID))
	if err != nil {
		if errors.Is(err, itemlock.ErrLocked) {
			return fmt.Errorf("session %s is already running or locked", session.ID)
		}
		return err
	}
	return lock.Release()
}
func (s *Service) SessionDir(itemID string) string {
	return filepath.Join(s.store.ItemDir(itemID), "sessions", "pi")
}
func (s *Service) LockPath(itemID, sessionID string) string {
	return filepath.Join(s.store.ItemDir(itemID), "locks", "pi-"+sessionID+".lock")
}
func ResolveConversation(m model.Manifest, selector string) (model.PiSession, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" || selector == "root" {
		if m.RootPiSession != nil && strings.TrimSpace(m.RootPiSession.ID) != "" {
			return *m.RootPiSession, nil
		}
		return model.PiSession{}, fmt.Errorf("work item has no root Pi session")
	}
	upper := strings.ToUpper(selector)
	if m.RootPiSession != nil && (m.RootPiSession.ID == upper || strings.HasPrefix(m.RootPiSession.ID, upper) || m.RootPiSession.Path == selector) {
		return *m.RootPiSession, nil
	}
	return model.PiSession{}, fmt.Errorf("session %q not found in manifest; use Pi directly inside the work item session directory for non-root sessions", selector)
}
