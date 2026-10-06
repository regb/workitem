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
	Conversation model.Conversation
	Manifest     model.Manifest
}

func (s *Service) CreateConversation(ctx context.Context, m model.Manifest, conversation model.Conversation) (CreateConversationResult, error) {
	if !m.Checkout.Present() || m.Checkout.Claim == nil || m.Checkout.Claim.Path == "" {
		return CreateConversationResult{}, fmt.Errorf("checkout is absent; run `wi workspace ensure` first")
	}
	if strings.TrimSpace(conversation.Driver) == "" || strings.TrimSpace(conversation.ID) == "" || strings.TrimSpace(conversation.NativeLocator()) == "" {
		return CreateConversationResult{}, fmt.Errorf("conversation driver, ID, and locator are required")
	}
	rel := filepath.FromSlash(conversation.NativeLocator())
	absolute, err := s.AbsPath(m.ID, rel)
	if err != nil {
		return CreateConversationResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return CreateConversationResult{}, fmt.Errorf("create conversation directory: %w", err)
	}
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return CreateConversationResult{}, fmt.Errorf("create conversation file: %w", err)
	}
	if err := file.Close(); err != nil {
		return CreateConversationResult{}, fmt.Errorf("close conversation file: %w", err)
	}
	now := s.now()
	conversation.Locator = filepath.ToSlash(rel)
	m.PrimaryConversation = &conversation
	m.UpdatedAt = now
	if err := s.store.SaveManifest(ctx, m); err != nil {
		_ = os.Remove(absolute)
		return CreateConversationResult{}, err
	}
	_ = s.store.AppendEvent(ctx, m.ID, model.NewEvent(now, "conversation.created", "wi", map[string]any{"conversation_id": conversation.ID, "locator": conversation.NativeLocator(), "driver": conversation.Driver, "primary": true}))
	return CreateConversationResult{Conversation: conversation, Manifest: m}, nil
}
func (s *Service) EnsureConversationUnlocked(m model.Manifest, session model.Conversation) error {
	lock, err := itemlock.TryAcquire(s.LockPath(m.ID, session.ID))
	if err != nil {
		if errors.Is(err, itemlock.ErrLocked) {
			return fmt.Errorf("conversation %s is already running or locked", session.ID)
		}
		return err
	}
	return lock.Release()
}
func (s *Service) ConversationDir(itemID string, conversation model.Conversation) string {
	return filepath.Dir(filepath.Join(s.store.ItemDir(itemID), filepath.FromSlash(conversation.NativeLocator())))
}
func (s *Service) LockPath(itemID, sessionID string) string {
	return filepath.Join(s.store.ItemDir(itemID), "locks", "conversation-"+sessionID+".lock")
}
func ResolveConversation(m model.Manifest, selector string) (model.Conversation, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" || selector == "root" {
		if m.PrimaryConversation != nil && strings.TrimSpace(m.PrimaryConversation.ID) != "" {
			return *m.PrimaryConversation, nil
		}
		return model.Conversation{}, fmt.Errorf("work item has no primary conversation")
	}
	upper := strings.ToUpper(selector)
	if m.PrimaryConversation != nil && (m.PrimaryConversation.ID == upper || strings.HasPrefix(m.PrimaryConversation.ID, upper) || m.PrimaryConversation.NativeLocator() == selector) {
		return *m.PrimaryConversation, nil
	}
	return model.Conversation{}, fmt.Errorf("conversation %q is not the work item's primary conversation", selector)
}
