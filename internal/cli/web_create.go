package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	webaccess "github.com/regb/workitem/internal/web"
)

// Create saves an untitled active item whose description is the prompt. The
// slow phase (title, checkout, runtime, prompt) runs later in StartCreated.
func (b webBackend) Create(ctx context.Context, request webaccess.Create) webaccess.Outcome {
	source, repository, err := b.projectRepository(ctx, request.ProjectID)
	if err != nil {
		return webaccess.Outcome{Error: err.Error()}
	}
	agentName := strings.TrimSpace(request.Agent)
	if agentName == "" {
		agentName = b.cfg.App.DefaultAgent
	}
	if err := b.cfg.App.ValidateAgentConfig(ctx, agentName); err != nil {
		return webaccess.Outcome{Error: err.Error()}
	}
	result, err := createCoordinatorWorkItem(ctx, b.cfg, app.NewWorkItemOptions{
		Untitled: true, Description: request.Prompt, CWD: b.cfg.CWD, Env: b.cfg.Env, Agent: agentName, RepositoryLabels: source.Labels,
		Repository: repository,
	})
	out := webaccess.Outcome{Warnings: result.Warnings}
	if err == nil {
		_, err = coordinatorStateTransition(ctx, b.cfg, result.Manifest.ID, model.StateActive, "work_item.started", false)
	}
	id := result.Manifest.ID
	if err != nil {
		out.Error = err.Error()
		if id == "" {
			out.CreationUncertain = true
			out.Warnings = append(out.Warnings, "Creation may have reached the daemon. Check the work list before retrying.")
			return out
		}
		out.Warnings = append(out.Warnings, "The created ticket is retained. Inspect it before retrying startup or prompt delivery.")
	}
	out.Observed = &result.Manifest
	if canonical, readErr := b.cfg.Coordinator.CanonicalManifest(ctx, id); readErr == nil {
		out.Revision = canonical.Revision
	}
	if manifest, readErr := b.reread(id); readErr == nil {
		out.Observed = &manifest
	} else {
		out.Warnings = append(out.Warnings, "Could not refresh lifecycle; showing the last confirmed created manifest: "+readErr.Error())
	}
	return out
}

// CreateBacklog saves a titled item to the backlog of a registered
// repository with the default agent profile; nothing starts.
func (b webBackend) CreateBacklog(ctx context.Context, request webaccess.BacklogCreate) webaccess.Outcome {
	source, repository, err := b.projectRepository(ctx, request.ProjectID)
	if err != nil {
		return webaccess.Outcome{Error: err.Error()}
	}
	result, err := createCoordinatorWorkItem(ctx, b.cfg, app.NewWorkItemOptions{
		Title: request.Title, Description: request.Description, CWD: b.cfg.CWD, Env: b.cfg.Env, Agent: b.cfg.App.DefaultAgent, RepositoryLabels: source.Labels,
		Repository: repository,
	})
	out := webaccess.Outcome{Warnings: result.Warnings}
	if err != nil {
		out.Error = err.Error()
		if result.Manifest.ID == "" {
			out.CreationUncertain = true
			out.Warnings = append(out.Warnings, "Creation may have reached the daemon. Check the backlog before retrying.")
		}
	}
	if result.Manifest.ID != "" {
		out.Observed = &result.Manifest
	}
	return out
}

// projectRepository resolves a project of the registry, rejecting unknown
// ones, and the repository a new item in it records: the integration branch
// at its current commit.
func (b webBackend) projectRepository(ctx context.Context, project string) (coordinator.RepositorySourceResult, *model.Repository, error) {
	source, err := b.cfg.Coordinator.RepositorySource(ctx, project, "")
	if err != nil {
		return source, nil, err
	}
	if source.Repository.ID != project {
		return source, nil, errors.New("unknown project; refresh the project picker")
	}
	return source, &model.Repository{ID: source.Repository.ID, Name: source.Repository.Name, IntegrationBranch: source.Repository.IntegrationBranch, RootAtCreation: source.GitDir, GitCommonDir: source.GitDir, CreatedFromCommit: source.Commit}, nil
}

// StartCreated performs the slow phase only for the already-durable new item.
// It reads the saved prompt instead of retaining unrestricted text in a queue.
func (b webBackend) StartCreated(ctx context.Context, id string, revision uint64) (out webaccess.Outcome) {
	// Nobody answers a question during background startup: one that needs
	// approval stops before the agent starts, and the conversation offers
	// to send the task, which asks in the browser.
	ctx, asked := webApprovals(ctx, "")
	defer func() {
		asked(&out)
		if m, err := b.reread(id); err == nil {
			out.Observed = &m
		}
		switch {
		case out.Approval != nil:
			out.Error = "The agent needs your approval to start (" + out.Approval.Question + "). Send the task from the conversation to answer."
		case out.Error != "":
			out.Warnings = append(out.Warnings, "Conversation retained. Inspect it before retrying startup or prompt delivery.")
		}
	}()
	fail := func(err error) webaccess.Outcome {
		out.Error = err.Error()
		return out
	}
	shown, err := b.cfg.App.ShowWorkItem(ctx, b.resolve(id))
	if err != nil {
		return fail(err)
	}
	canonical, err := b.cfg.Coordinator.CanonicalManifest(ctx, id)
	if err != nil {
		return fail(err)
	}
	// Only start the item exactly as it was queued: any edit, start, shelve
	// or close in the meantime means someone else took it over. Without a
	// queued revision, it must at least still wait for its first runtime.
	queued := canonical.Manifest
	if revision != 0 && canonical.Revision != revision || queued.State != model.StateActive || queued.PrimaryConversation != nil || shown.Manifest.PrimaryConversation != nil {
		return fail(errors.New("Conversation changed while queued; background startup was not applied."))
	}
	titleCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	title, titleErr := b.cfg.App.GenerateConversationTitle(titleCtx, shown.Description)
	cancel()
	if titleErr != nil || strings.TrimSpace(title) == "" {
		title = summaryTitle(shown.Description)
		out.Warnings = append(out.Warnings, "Automatic title unavailable; using a task summary.")
	}
	commandID, err := model.NewID()
	if err != nil {
		return fail(err)
	}
	// Main keeps the creation-time slug and branch stable. Only the display
	// title changes, fenced by the revision captured before generation.
	renamed, err := b.cfg.Coordinator.EditItem(ctx, coordinator.EditItemRequest{Command: coordinator.ManifestCommand{ID: commandID, ProtocolVersion: coordinator.ProtocolVersion, Type: coordinator.CommandEdit, ItemID: id, ExpectedRevision: &canonical.Revision, Actor: "user", Edit: &coordinator.MetadataEdit{Title: &title}, CreatedAt: time.Now().UTC()}})
	if err != nil {
		return fail(err)
	}
	resolve := b.resolve(id)
	resolve.CWD = renamed.Manifest.Repository.OperationalRoot()
	started, err := b.cfg.App.EnsureNewAgentRuntime(ctx, resolve, agent.ModeHeadless)
	out.Warnings = append(out.Warnings, started.Warnings...)
	if err != nil {
		return fail(err)
	}
	if err = b.cfg.App.WaitForAgentRuntimeReady(ctx, id, &started.Runtime); err != nil {
		return fail(err)
	}
	submitted, err := b.cfg.App.AgentControl(ctx, app.AgentControlOptions{ResolveOptions: resolve, CommandType: agent.CommandPrompt, Message: shown.Description, Actor: "operator", ExpectedRuntimeID: started.Runtime.ID, RequireIdle: true})
	if err != nil {
		return fail(err)
	}
	if err := b.confirmInitialPrompt(ctx, id, started.Runtime.ID, submitted.Command.ID); err != nil {
		return fail(err)
	}
	return out
}

// confirmInitialPrompt watches briefly for an immediate rejection. Control
// acknowledgement is transport handoff, not native acceptance; it never waits
// for the model turn, and silence within the window counts as delivered.
func (b webBackend) confirmInitialPrompt(ctx context.Context, id, runtimeID, commandID string) error {
	confirmCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var sequence uint64
	for {
		result, err := b.cfg.Coordinator.RuntimeEvents(confirmCtx, coordinator.RuntimeEventsRequest{ItemID: id, AfterSequence: sequence, Limit: 256, WaitMillis: 1000})
		if err != nil {
			if errors.Is(confirmCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
				return nil
			}
			return errors.New("Initial prompt handoff could not be confirmed. Inspect history before resending.")
		}
		for _, event := range result.Events {
			if matched, err := webInitialPromptDisposition(event, runtimeID, commandID); matched {
				return err
			}
		}
		sequence = result.LastSequence
	}
}

func webInitialPromptDisposition(event coordinator.DomainEvent, runtimeID, commandID string) (bool, error) {
	var payload struct {
		RuntimeID string `json:"runtime_id"`
		CommandID string `json:"command_id"`
	}
	if json.Unmarshal(event.Payload, &payload) != nil || payload.RuntimeID != runtimeID {
		return false, nil
	}
	if event.Type == agent.EventRuntimeFailed {
		return true, fmt.Errorf("Agent failed during initial prompt delivery. Inspect the retained conversation before retrying.")
	}
	if payload.CommandID != commandID || commandID == "" {
		return false, nil
	}
	switch event.Type {
	case agent.EventCommandAccepted:
		return true, nil
	case agent.EventCommandRejected:
		return true, fmt.Errorf("Agent rejected the initial prompt. Inspect the retained conversation before retrying.")
	}
	return false, nil
}

// summaryTitle is the fallback title: the prompt's first words.
func summaryTitle(prompt string) string {
	words := strings.Fields(prompt)
	if len(words) > 8 {
		words = words[:8]
	}
	runes := []rune(strings.Join(words, " "))
	if len(runes) > 96 {
		runes = runes[:96]
	}
	if len(runes) == 0 {
		return "New conversation"
	}
	return string(runes)
}
