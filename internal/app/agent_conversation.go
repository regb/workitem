package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agentcore "github.com/regb/workitem/internal/app/core/primaryagent"
	"github.com/regb/workitem/internal/model"
)

type createPiSessionResult = agentcore.CreateConversationResult

func (a *App) createPiSession(ctx context.Context, m model.Manifest) (createPiSessionResult, error) {
	return a.primaryAgentService().CreateConversation(ctx, m)
}
func (a *App) ensurePiSessionNotRunning(m model.Manifest, session model.PiSession) error {
	return a.primaryAgentService().EnsureConversationUnlocked(m, session)
}
func (a *App) ensureConversationCheckout(ctx context.Context, m model.Manifest) (model.Manifest, model.PiSession, []string, error) {
	if m.RootPiSession == nil {
		created, err := a.createPiSession(ctx, m)
		return created.Manifest, created.Session, nil, err
	}
	session := *m.RootPiSession
	if !m.Checkout.Present() || m.Checkout.Path == nil || strings.TrimSpace(*m.Checkout.Path) == "" {
		return m, session, nil, nil
	}
	sourcePath, err := a.primaryAgentService().AbsPath(m.ID, session.Path)
	if err != nil {
		return model.Manifest{}, model.PiSession{}, nil, err
	}
	if info, err := os.Stat(sourcePath); err == nil && info.Size() == 0 {
		return m, session, nil, nil
	}
	if a.Pi == nil {
		return m, session, []string{"Pi adapter is unavailable; could not verify session header checkout"}, nil
	}
	sessionCWD, err := a.Pi.SessionCWD(sourcePath)
	if err != nil {
		return model.Manifest{}, model.PiSession{}, nil, err
	}
	checkoutPath := strings.TrimSpace(*m.Checkout.Path)
	if sessionCWD == "" || agentcore.CheckoutContainsPath(checkoutPath, sessionCWD) {
		return m, session, nil, nil
	}
	if err := a.ensurePiSessionNotRunning(m, session); err != nil {
		return model.Manifest{}, model.PiSession{}, nil, err
	}
	id, err := a.NewID()
	if err != nil {
		return model.Manifest{}, model.PiSession{}, nil, err
	}
	rel := filepath.Join("sessions", "pi", id+".jsonl")
	targetPath, err := a.primaryAgentService().AbsPath(m.ID, rel)
	if err != nil {
		return model.Manifest{}, model.PiSession{}, nil, err
	}
	if err := a.Pi.ForkSession(ctx, sourcePath, targetPath, checkoutPath); err != nil {
		return model.Manifest{}, model.PiSession{}, nil, fmt.Errorf("fork Pi session for reassigned checkout: %w", err)
	}
	now := a.now()
	forked := model.PiSession{ID: id, Path: filepath.ToSlash(rel)}
	m.RootPiSession = &forked
	m.UpdatedAt = now
	if err := a.Store.SaveManifest(ctx, m); err != nil {
		_ = os.Remove(targetPath)
		return model.Manifest{}, model.PiSession{}, nil, err
	}
	warnings := []string{}
	if err := a.Store.AppendEvent(ctx, m.ID, model.NewEvent(now, "pi_session.forked_for_checkout", "wi", map[string]any{"source_session_id": session.ID, "source_path": session.Path, "source_cwd": sessionCWD, "session_id": forked.ID, "path": forked.Path, "checkout_path": checkoutPath})); err != nil {
		warnings = append(warnings, "could not append pi_session.forked_for_checkout event: "+err.Error())
	}
	return m, forked, warnings, nil
}

func (a *App) validateConversationCheckout(m model.Manifest, session model.PiSession) error {
	if !m.Checkout.Present() || m.Checkout.Path == nil || strings.TrimSpace(*m.Checkout.Path) == "" || a.Pi == nil {
		return nil
	}
	path, err := a.primaryAgentService().AbsPath(m.ID, session.Path)
	if err != nil {
		return err
	}
	sessionCWD, err := a.Pi.SessionCWD(path)
	if err != nil {
		return err
	}
	checkoutPath := strings.TrimSpace(*m.Checkout.Path)
	if sessionCWD != "" && !agentcore.CheckoutContainsPath(checkoutPath, sessionCWD) {
		return fmt.Errorf("Pi session %s belongs to %s, but work item %s now owns checkout %s; stop the runtime so wi can fork the session into the current checkout", session.ID, sessionCWD, m.ID, checkoutPath)
	}
	return nil
}
