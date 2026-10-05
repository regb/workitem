package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	agentcore "github.com/regb/workitem/internal/app/core/primaryagent"
	"github.com/regb/workitem/internal/model"
)

type createConversationResult = agentcore.CreateConversationResult

func (a *App) createConversation(ctx context.Context, m model.Manifest) (createConversationResult, error) {
	agentName := m.Agent
	if agentName == "" {
		agentName = "default"
	}
	definition, ok := a.AgentsConfig[agentName]
	if !ok || strings.TrimSpace(definition.Driver) == "" {
		return createConversationResult{}, fmt.Errorf("agent definition %q has no conversation driver", agentName)
	}
	backend := a.ConversationBackends[definition.Driver]
	if backend == nil {
		return createConversationResult{}, fmt.Errorf("conversation backend for driver %q is not configured", definition.Driver)
	}
	id, err := a.NewID()
	if err != nil {
		return createConversationResult{}, err
	}
	driverConfig := map[string]any{}
	for key, value := range a.DriversConfig[definition.Driver].Defaults {
		driverConfig[key] = value
	}
	for key, value := range definition.Config {
		driverConfig[key] = value
	}
	configJSON, err := json.Marshal(driverConfig)
	if err != nil {
		return createConversationResult{}, err
	}
	conversation, err := backend.NewConversation(ctx, id, agentName, configJSON)
	if err != nil {
		return createConversationResult{}, err
	}
	return a.primaryAgentService().CreateConversation(ctx, m, conversation)
}

func (a *App) observeConversation(ctx context.Context, m model.Manifest, now time.Time) (agentcore.ConversationStatus, []string) {
	if m.PrimaryConversation == nil {
		return agentcore.ConversationStatus{UnavailableReason: "no primary conversation is recorded"}, nil
	}
	conversation := *m.PrimaryConversation
	backend := a.conversationBackend(m, conversation)
	if backend == nil {
		return agentcore.ConversationStatus{ID: conversation.ID, PathRel: conversation.NativeLocator(), UnavailableReason: "conversation driver is unavailable"}, nil
	}
	path, err := a.primaryAgentService().AbsPath(m.ID, conversation.NativeLocator())
	if err != nil {
		return agentcore.ConversationStatus{ID: conversation.ID, PathRel: conversation.NativeLocator(), UnavailableReason: err.Error()}, nil
	}
	observed, warnings, err := backend.ObserveConversation(ctx, conversation, path, now)
	if err != nil {
		return agentcore.ConversationStatus{ID: conversation.ID, Path: path, PathRel: conversation.NativeLocator(), UnavailableReason: err.Error()}, warnings
	}
	return conversationStatusFromDriver(conversation, path, observed), warnings
}

func conversationStatusFromDriver(conversation model.Conversation, path string, observed driverprotocol.ConversationObservation) agentcore.ConversationStatus {
	result := agentcore.ConversationStatus{ID: conversation.ID, Path: path, PathRel: conversation.NativeLocator(), Source: observed.Source, Exists: observed.Exists, SizeBytes: observed.SizeBytes, ModTime: observed.ModTime, EntriesScanned: observed.EntriesScanned, MalformedLines: observed.MalformedLines, LastActivityAgeSeconds: observed.LastActivityAgeSeconds, InferredTurnState: observed.InferredTurnState, UnavailableReason: observed.UnavailableReason}
	convert := func(fact *driverprotocol.ConversationFact) *agentcore.ConversationEventSummary {
		if fact == nil {
			return nil
		}
		return &agentcore.ConversationEventSummary{Line: int(fact.Line), Type: fact.Type, Timestamp: fact.Timestamp, Role: fact.Role, StopReason: fact.StopReason, ContentTypes: fact.ContentTypes, Terminal: fact.Terminal, Failed: fact.Failed}
	}
	result.LastEvent = convert(observed.LastEvent)
	result.LastTurnActivity = convert(observed.LastTurnActivity)
	result.LastUserPrompt = convert(observed.LastUserPrompt)
	result.LastAssistantMessage = convert(observed.LastAssistantMessage)
	result.LastTerminalAssistant = convert(observed.LastTerminalAssistant)
	result.LastToolActivity = convert(observed.LastToolActivity)
	return result
}

func (a *App) conversationBackend(m model.Manifest, conversation model.Conversation) ConversationBackend {
	driver := conversation.Driver
	if driver == "" {
		agentName := m.Agent
		if agentName == "" {
			agentName = "default"
		}
		driver = a.AgentsConfig[agentName].Driver
	}
	return a.ConversationBackends[driver]
}

func (a *App) ensureConversationNotRunning(m model.Manifest, conversation model.Conversation) error {
	return a.primaryAgentService().EnsureConversationUnlocked(m, conversation)
}

func (a *App) ensureConversationCheckout(ctx context.Context, m model.Manifest) (model.Manifest, model.Conversation, []string, error) {
	if m.PrimaryConversation == nil {
		created, err := a.createConversation(ctx, m)
		return created.Manifest, created.Conversation, nil, err
	}
	conversation := *m.PrimaryConversation
	if !m.Checkout.Present() || m.Checkout.Claim == nil || strings.TrimSpace(m.Checkout.Claim.Path) == "" {
		return m, conversation, nil, nil
	}
	sourcePath, err := a.primaryAgentService().AbsPath(m.ID, conversation.NativeLocator())
	if err != nil {
		return model.Manifest{}, model.Conversation{}, nil, err
	}
	if info, err := os.Stat(sourcePath); err == nil && info.Size() == 0 {
		return m, conversation, nil, nil
	}
	backend := a.conversationBackend(m, conversation)
	if backend == nil {
		return m, conversation, []string{"conversation backend is unavailable; could not verify its checkout"}, nil
	}
	conversationCWD, err := backend.ConversationCWD(ctx, sourcePath)
	if err != nil {
		return model.Manifest{}, model.Conversation{}, nil, err
	}
	checkoutPath := strings.TrimSpace(m.Checkout.Claim.Path)
	if conversationCWD == "" || agentcore.CheckoutContainsPath(checkoutPath, conversationCWD) {
		return m, conversation, nil, nil
	}
	if err := a.ensureConversationNotRunning(m, conversation); err != nil {
		return model.Manifest{}, model.Conversation{}, nil, err
	}
	id, err := a.NewID()
	if err != nil {
		return model.Manifest{}, model.Conversation{}, nil, err
	}
	agentName := m.Agent
	if agentName == "" {
		agentName = "default"
	}
	definition := a.AgentsConfig[agentName]
	driverConfig := map[string]any{}
	for key, value := range a.DriversConfig[definition.Driver].Defaults {
		driverConfig[key] = value
	}
	for key, value := range definition.Config {
		driverConfig[key] = value
	}
	configJSON, err := json.Marshal(driverConfig)
	if err != nil {
		return model.Manifest{}, model.Conversation{}, nil, err
	}
	forked, err := backend.NewConversation(ctx, id, agentName, configJSON)
	if err != nil {
		return model.Manifest{}, model.Conversation{}, nil, err
	}
	targetPath, err := a.primaryAgentService().AbsPath(m.ID, forked.NativeLocator())
	if err != nil {
		return model.Manifest{}, model.Conversation{}, nil, err
	}
	if err := backend.ForkConversation(ctx, sourcePath, targetPath, checkoutPath); err != nil {
		return model.Manifest{}, model.Conversation{}, nil, fmt.Errorf("fork conversation for reassigned checkout: %w", err)
	}
	now := a.now()
	m.PrimaryConversation = &forked
	m.UpdatedAt = now
	if err := a.Store.SaveManifest(ctx, m); err != nil {
		_ = os.Remove(targetPath)
		return model.Manifest{}, model.Conversation{}, nil, err
	}
	warnings := []string{}
	if err := a.Store.AppendEvent(ctx, m.ID, model.NewEvent(now, "conversation.forked_for_checkout", "wi", map[string]any{"source_conversation_id": conversation.ID, "source_locator": conversation.NativeLocator(), "source_cwd": conversationCWD, "conversation_id": forked.ID, "locator": forked.NativeLocator(), "checkout_path": checkoutPath})); err != nil {
		warnings = append(warnings, "could not append conversation.forked_for_checkout event: "+err.Error())
	}
	return m, forked, warnings, nil
}

func (a *App) validateConversationCheckout(m model.Manifest, conversation model.Conversation) error {
	if !m.Checkout.Present() || m.Checkout.Claim == nil || strings.TrimSpace(m.Checkout.Claim.Path) == "" {
		return nil
	}
	backend := a.conversationBackend(m, conversation)
	if backend == nil {
		return nil
	}
	path, err := a.primaryAgentService().AbsPath(m.ID, conversation.NativeLocator())
	if err != nil {
		return err
	}
	conversationCWD, err := backend.ConversationCWD(context.Background(), path)
	if err != nil {
		return err
	}
	checkoutPath := strings.TrimSpace(m.Checkout.Claim.Path)
	if conversationCWD != "" && !agentcore.CheckoutContainsPath(checkoutPath, conversationCWD) {
		return fmt.Errorf("conversation %s belongs to %s, but work item %s now owns checkout %s; stop the runtime so wi can fork it into the current checkout", conversation.ID, conversationCWD, m.ID, checkoutPath)
	}
	return nil
}
