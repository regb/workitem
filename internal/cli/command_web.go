package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	viewapp "github.com/regb/workitem/internal/app/view"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	webaccess "github.com/regb/workitem/internal/web"
)

func runWeb(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("web", cfg.Stderr)
	var port int
	var accessURL string
	fs.IntVar(&port, "port", 0, "loopback port (default: choose an available port)")
	fs.StringVar(&accessURL, "access-url", "", "browser-facing HTTPS origin for a trusted reverse proxy")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if fs.NArg() != 0 || port < 0 || port > 65535 || jsonOut {
		return usageErr{fmt.Errorf("usage: wi web [--port PORT] [--access-url HTTPS_ORIGIN]")}
	}
	if accessURL != "" && !strings.HasPrefix(accessURL, "https://") {
		return usageErr{fmt.Errorf("--access-url requires an https:// origin")}
	}
	if cfg.Coordinator == nil || cfg.ControlAccess != coordinator.AccessOperator {
		return fmt.Errorf("wi web requires the local operator daemon endpoint")
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	defer listener.Close()
	// Browser access is never the caller's current tmux pane.
	env := map[string]string{}
	for k, v := range cfg.Env {
		if k != "TMUX" && k != "TMUX_PANE" {
			env[k] = v
		}
	}
	cfg.Env = env
	var handler *webaccess.Server
	if accessURL == "" {
		handler, err = webaccess.New(webBackend{cfg}, listener.Addr().String())
	} else {
		handler, err = webaccess.NewWithAccessURL(webBackend{cfg}, listener.Addr().String(), accessURL)
	}
	if err != nil {
		return err
	}
	defer handler.Close()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	fmt.Fprintf(cfg.Stdout, "Open this local access URL:\n%s\nKeep it private. Ctrl-C stops serving, not agents.\n", handler.URL())
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
			_ = server.Close()
		case <-done:
		}
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

type webBackend struct{ cfg Config }

func (b webBackend) resolve(id string) app.ResolveOptions {
	return app.ResolveOptions{Selector: id, CWD: b.cfg.CWD, Env: b.cfg.Env}
}
func (b webBackend) List(ctx context.Context) (webaccess.List, error) {
	_, err := b.cfg.Coordinator.ActivityBarrier(ctx)
	if err != nil {
		return webaccess.List{}, err
	}
	rules, err := effectiveListLabelRules(b.cfg.App.ListConfig.Labels, b.cfg.Env, nil)
	if err != nil {
		return webaccess.List{}, err
	}
	projection, err := b.cfg.Coordinator.ManifestProjection(ctx)
	if err != nil {
		return webaccess.List{}, err
	}
	result := viewapp.New(projectedManifestStore{manifests: projection.Manifests}).WorkList(app.WorkListOptions{LabelRules: rules}, b.cfg.App.DeepWorkConfig.MaxActive, b.cfg.App.ListConfig.RepositoryFolders)
	result.Warnings = append(result.Warnings, projection.Projection.Warnings...)
	if !projection.Projection.Fresh {
		result.Warnings = append(result.Warnings, "daemon manifest projection is stale")
	}
	agents, warnings := enrichWorkListAgentStatus(ctx, b.cfg, &result)
	result.Warnings = append(result.Warnings, warnings...)
	list, err := webList(result, agents, projection.Manifests)
	if err != nil {
		return list, err
	}
	list.Projects, err = b.Projects(ctx)
	return list, err
}

// The registry, not item provenance or host folders, owns project identity.
func webProjects(repositories []model.OwnedRepository) []webaccess.Project {
	projects := []webaccess.Project{}
	for _, repository := range repositories {
		projects = append(projects, webaccess.Project{ID: repository.ID, Name: repository.Name})
	}
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].Name != projects[j].Name {
			return projects[i].Name < projects[j].Name
		}
		return projects[i].ID < projects[j].ID
	})
	return projects
}

func (b webBackend) Projects(ctx context.Context) ([]webaccess.Project, error) {
	repositories, err := b.cfg.Coordinator.ListRepositories(ctx)
	if err != nil {
		return nil, err
	}
	return webProjects(repositories), nil
}

func (b webBackend) Agents(context.Context) ([]webaccess.AgentProfile, error) {
	profiles := make([]webaccess.AgentProfile, 0, len(b.cfg.App.AgentsConfig))
	for name, definition := range b.cfg.App.AgentsConfig {
		label := strings.TrimSpace(definition.Label)
		if label == "" {
			label = name
		}
		profiles = append(profiles, webaccess.AgentProfile{Name: name, Label: label})
	}
	sort.Slice(profiles, func(i, j int) bool {
		if (profiles[i].Name == "default") != (profiles[j].Name == "default") {
			return profiles[i].Name == "default"
		}
		if profiles[i].Label != profiles[j].Label {
			return profiles[i].Label < profiles[j].Label
		}
		return profiles[i].Name < profiles[j].Name
	})
	return profiles, nil
}

func (b webBackend) Create(ctx context.Context, request webaccess.Create) webaccess.Outcome {
	projects, err := b.Projects(ctx)
	if err != nil {
		return webaccess.Outcome{Error: err.Error()}
	}
	project, err := webCreationProject(projects, request.ProjectID)
	if err != nil {
		return webaccess.Outcome{Error: err.Error()}
	}
	var source coordinator.RepositorySourceResult
	if err := b.cfg.Coordinator.Call(ctx, coordinator.MethodRepositorySource, coordinator.RepositorySourceRequest{Selector: project.ID}, &source); err != nil {
		return webaccess.Outcome{Error: err.Error()}
	}
	agentName := strings.TrimSpace(request.Agent)
	if agentName == "" {
		agentName = "default"
	}
	result, err := createCoordinatorWorkItem(ctx, b.cfg, app.NewWorkItemOptions{
		Title: "New conversation", Description: request.Prompt, CWD: b.cfg.CWD, Env: b.cfg.Env, Agent: agentName, RepositoryLabels: source.Labels,
		Repository: &model.Repository{ID: source.Repository.ID, Name: source.Repository.Name, IntegrationBranch: source.Repository.IntegrationBranch, RootAtCreation: source.GitDir, GitCommonDir: source.GitDir, RemoteURL: source.Repository.OriginURL, CreatedFromCommit: source.Commit},
	})
	out := webaccess.Outcome{Warnings: result.Warnings}
	if err == nil {
		_, err = coordinatorStateTransition(ctx, b.cfg, result.Manifest.ID, model.StateWorking, "work_item.started", false)
	}
	if err != nil {
		out.Error = err.Error()
		if result.Manifest.ID == "" {
			out.CreationUncertain = true
			out.Warnings = append(out.Warnings, "Creation may have reached the daemon. Check the work list before retrying.")
		}
	}
	if id := result.Manifest.ID; id != "" {
		out.Observed = &result.Manifest
		inspect, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if manifest, readErr := b.cfg.App.ResolveItem(inspect, b.resolve(id)); readErr == nil {
			out.Observed = &manifest
		} else {
			out.Warnings = append(out.Warnings, "Could not refresh lifecycle; showing the last confirmed created manifest: "+readErr.Error())
		}
		if err != nil {
			out.Warnings = append(out.Warnings, "The created ticket is retained. Inspect it before retrying startup or prompt delivery.")
		}
	}
	return out
}

// StartCreated performs the slow phase only for the already-durable new item.
// It reads the saved prompt instead of retaining unrestricted text in a queue.
func (b webBackend) StartCreated(ctx context.Context, id string) (out webaccess.Outcome) {
	defer func() {
		inspect, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if m, err := b.cfg.App.ResolveItem(inspect, b.resolve(id)); err == nil {
			out.Observed = &m
		}
		if out.Error != "" {
			out.Warnings = append(out.Warnings, "Conversation retained. Inspect it before retrying startup or prompt delivery.")
		}
	}()
	shown, err := b.cfg.App.ShowWorkItem(ctx, b.resolve(id))
	if err != nil {
		out.Error = err.Error()
		return
	}
	if shown.Manifest.State != model.StateWorking || shown.Manifest.PrimaryConversation != nil {
		out.Error = "Conversation changed while queued; startup was not applied."
		return
	}
	titleCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	title, titleErr := b.cfg.App.GenerateConversationTitle(titleCtx, shown.Description)
	cancel()
	if titleErr != nil || strings.TrimSpace(title) == "" {
		words := strings.Fields(shown.Description)
		if len(words) > 8 {
			words = words[:8]
		}
		title = strings.Join(words, " ")
		runes := []rune(title)
		if len(runes) > 96 {
			title = string(runes[:96])
		}
		if title == "" {
			title = "New conversation"
		}
		out.Warnings = append(out.Warnings, "Automatic title unavailable; using a task summary.")
	}
	canonical, err := b.cfg.Coordinator.CanonicalManifest(ctx, id)
	if err != nil {
		out.Error = err.Error()
		return
	}
	if canonical.Manifest.State != model.StateWorking || canonical.Manifest.PrimaryConversation != nil || canonical.Manifest.Title != shown.Manifest.Title || canonical.Manifest.Slug != shown.Manifest.Slug || canonical.Manifest.Checkout.Specification.Branch != shown.Manifest.Checkout.Specification.Branch {
		out.Error = "Conversation changed while queued; background startup was not applied."
		return
	}
	commandID, err := model.NewID()
	if err != nil {
		out.Error = err.Error()
		return
	}
	renamed, err := b.cfg.Coordinator.ExecuteManifestCommand(ctx, coordinator.ManifestCommand{ID: commandID, ProtocolVersion: coordinator.ProtocolVersion, Type: coordinator.CommandNamingFinalize, ItemID: id, ExpectedRevision: &canonical.Revision, Actor: "user", Title: title, CreatedAt: time.Now().UTC()})
	if err != nil {
		out.Error = err.Error()
		return
	}
	resolve := b.resolve(id)
	resolve.CWD = renamed.Manifest.Repository.OperationalRoot()
	started, err := b.cfg.App.EnsureNewAgentRuntime(ctx, resolve, agent.ModeHeadless)
	out.Warnings = append(out.Warnings, started.Warnings...)
	if err != nil {
		out.Error = err.Error()
		return
	}
	if err = b.cfg.App.WaitForAgentRuntimeReady(ctx, id, &started.Runtime); err != nil {
		out.Error = err.Error()
		return
	}
	submitted, err := b.cfg.App.AgentControl(ctx, app.AgentControlOptions{ResolveOptions: resolve, CommandType: agent.CommandPrompt, Message: shown.Description, Actor: "operator", ExpectedRuntimeID: started.Runtime.ID, RequireIdle: true})
	if err != nil {
		out.Error = err.Error()
		return
	}
	// Control acknowledgement is transport handoff, not native acceptance.
	// Watch briefly for immediate rejection; never wait for the model turn.
	confirmCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var sequence uint64
	for {
		result, eventErr := b.cfg.Coordinator.RuntimeEvents(confirmCtx, coordinator.RuntimeEventsRequest{ItemID: id, AfterSequence: sequence, Limit: 256, WaitMillis: 1000})
		if eventErr != nil {
			if errors.Is(confirmCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
				return
			}
			out.Error = "Initial prompt handoff could not be confirmed. Inspect history before resending."
			return
		}
		for _, event := range result.Events {
			if matched, dispositionErr := webInitialPromptDisposition(event, started.Runtime.ID, submitted.Command.ID); matched {
				if dispositionErr != nil {
					out.Error = dispositionErr.Error()
				}
				return
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

func webCreationProject(projects []webaccess.Project, id string) (webaccess.Project, error) {
	for _, project := range projects {
		if project.ID == id {
			return project, nil
		}
	}
	return webaccess.Project{}, fmt.Errorf("unknown project; refresh the project picker")
}

func webProject(repo model.Repository) webaccess.Project {
	name := repo.Name
	if name == "" {
		name = filepath.Base(repo.OperationalRoot())
	}
	return webaccess.Project{ID: model.RepositoryKey(repo), Name: name}
}

// Reuse the CLI presentation buckets and ordering rather than inventing a web policy.
func webList(result app.WorkListResult, agents map[string]string, manifests []model.Manifest) (webaccess.List, error) {
	// List rows intentionally omit full manifests. Read identity from the same
	// projection used to build the list, never from display names or zero values.
	repositories := map[string]model.Repository{}
	for _, manifest := range manifests {
		repositories[manifest.ID] = manifest.Repository
	}
	list := webaccess.List{WorkListResult: result, DisplaySections: []webaccess.Section{}, Projects: []webaccess.Project{}}
	projects := map[string]webaccess.Project{}
	for _, section := range workDisplaySections(result, "active", agents) {
		rows := []webaccess.ListItem{}
		for _, item := range section.Items {
			repository, found := repositories[item.ID]
			if !found {
				return webaccess.List{}, fmt.Errorf("repository metadata unavailable for item %s", item.ID)
			}
			project := webProject(repository)
			projects[project.ID] = project
			rows = append(rows, webaccess.ListItem{WorkListItem: item, ProjectID: project.ID})
		}
		list.DisplaySections = append(list.DisplaySections, webaccess.Section{Name: section.Name, Items: rows})
	}
	for _, project := range projects {
		list.Projects = append(list.Projects, project)
	}
	sort.Slice(list.Projects, func(i, j int) bool {
		if list.Projects[i].Name != list.Projects[j].Name {
			return list.Projects[i].Name < list.Projects[j].Name
		}
		return list.Projects[i].ID < list.Projects[j].ID
	})
	return list, nil
}

// Project scope only narrows the canonical queue; ranking and wrap policy stay shared.
func selectWebNext(queue coordinator.ActionabilityQueueResult, current string, projects []string) string {
	if projects != nil {
		allowed := map[string]bool{}
		for _, project := range projects {
			allowed[project] = true
		}
		filtered := []coordinator.ActionabilityQueueCandidate{}
		for _, candidate := range queue.Candidates {
			if allowed[model.RepositoryKey(candidate.Manifest.Repository)] {
				filtered = append(filtered, candidate)
			}
		}
		queue.Candidates = filtered
	}
	return coordinator.SelectActionability(queue, current).WorkItemID
}

func (b webBackend) Next(ctx context.Context, current string, projects []string) (string, error) {
	rules, err := effectiveListLabelRules(b.cfg.App.ListConfig.Labels, b.cfg.Env, nil)
	if err != nil {
		return "", err
	}
	result, err := b.cfg.Coordinator.ActionabilitySnapshot(ctx, coordinator.ActionabilitySnapshotRequest{CurrentItemID: current, Queue: coordinator.ActionabilityQueueOptions{Strategy: b.cfg.App.AttentionConfig.Priority, LabelRules: rules}})
	if err != nil {
		return "", err
	}
	return selectWebNext(result.Queue, current, projects), nil
}
func (b webBackend) Chat(ctx context.Context, id string) (webaccess.Chat, error) {
	shown, err := b.cfg.App.ShowWorkItem(ctx, b.resolve(id))
	if err != nil {
		return webaccess.Chat{}, err
	}
	m := shown.Manifest
	runtime, err := b.cfg.App.AgentRuntimeStatus(ctx, b.resolve(id))
	if err != nil {
		return webaccess.Chat{}, err
	}
	result := webaccess.Chat{Item: m, Description: shown.Description, Project: webProject(m.Repository), Runtime: runtime, Status: "unknown", History: driverprotocol.History{Messages: []driverprotocol.HistoryMessage{}}}
	observation, err := b.cfg.Coordinator.AgentObservation(ctx, id)
	if err == nil && observation.Found {
		result.Status = observation.Observation.Status
		if !observation.Projection.Fresh {
			result.Warnings = append(result.Warnings, "Agent observation is stale.")
		}
	}
	leaf := ""
	if runtime.Online {
		content, contentErr := b.cfg.App.AgentContent(ctx, b.resolve(id))
		if contentErr == nil {
			leaf = content.LeafID
			result.IdleGuard = content.IdleGuard
		} else {
			result.Warnings = append(result.Warnings, contentErr.Error())
		}
	}
	history, err := b.cfg.App.ConversationHistory(ctx, b.resolve(id), leaf)
	if err != nil {
		result.Warnings = append(result.Warnings, "History unavailable: "+err.Error())
	} else {
		result.History = history
	}
	if m.PrimaryConversation != nil && leaf == "" && runtime.Online && runtime.Runtime.Mode == "tui" {
		result.Warnings = append(result.Warnings, "History follows the last persisted branch; an older TUI's in-memory tree selection cannot be observed.")
	}
	if m.State == "archived" {
		result.Warnings = append(result.Warnings, "Archived conversations are read-only.")
	} else if !runtime.Online {
		result.Warnings = append(result.Warnings, "Start or resume this item with the CLI in headless mode. Opening chat never launches or replaces an owner.")
	}
	return result, nil
}
func (b webBackend) Content(ctx context.Context, id string) (driverprotocol.Content, error) {
	return b.cfg.App.AgentContent(ctx, b.resolve(id))
}
func (b webBackend) Send(ctx context.Context, id string, message webaccess.Message) webaccess.Outcome {
	m, err := b.cfg.App.ResolveItem(ctx, b.resolve(id))
	if err != nil {
		return webaccess.Outcome{Error: err.Error()}
	}
	if m.State == "archived" {
		return webaccess.Outcome{Error: "Archived conversations are read-only."}
	}
	content, contentErr := b.cfg.App.AgentContent(ctx, b.resolve(id))
	if message.Delivery == "prompt" && (contentErr != nil || !content.IdleGuard) {
		return webaccess.Outcome{Error: "The agent is not idle. Queue this message explicitly, or wait for the current turn to finish."}
	}
	if message.Delivery == "follow_up" && contentErr == nil && content.IdleGuard {
		return webaccess.Outcome{Error: "The agent is idle. Use Send now so the message starts a turn instead of waiting in an empty follow-up queue."}
	}
	result, err := b.cfg.App.AgentControl(ctx, app.AgentControlOptions{ResolveOptions: b.resolve(id), CommandType: message.Delivery, Message: message.Text, Images: message.Images, Actor: "operator", ExpectedRuntimeID: message.RuntimeID, RequireIdle: message.Delivery == "prompt"})
	out := webaccess.Outcome{Result: result}
	if err != nil {
		out.Error = err.Error()
	}
	return out
}
func (b webBackend) Shelve(ctx context.Context, id string) webaccess.Outcome {
	resolve := b.resolve(id)
	m, err := b.cfg.App.ResolveItem(ctx, resolve)
	if err != nil {
		return webaccess.Outcome{Error: err.Error()}
	}
	if m.State != model.StateWorking && m.State != model.StateWaiting {
		return webaccess.Outcome{Error: "Only working or waiting tickets can be shelved."}
	}
	warnings := []string{}
	owner, err := b.cfg.App.AgentRuntimeStatus(ctx, resolve)
	if err != nil {
		return webaccess.Outcome{Error: err.Error()}
	}
	if owner.Online {
		content, contentErr := b.cfg.App.AgentContent(ctx, resolve)
		if contentErr != nil || !content.IdleGuard {
			return webaccess.Outcome{Error: "The agent is still working. Wait for the current turn to settle before shelving."}
		}
		stopped, stopErr := b.cfg.App.StopAgentRuntime(ctx, resolve, false)
		warnings = append(warnings, stopped.Warnings...)
		if stopErr != nil {
			return webaccess.Outcome{Error: stopErr.Error(), Warnings: warnings, Observed: &m}
		}
		deadline := time.Now().Add(8 * time.Second)
		for {
			status, statusErr := b.cfg.App.AgentRuntimeStatus(ctx, resolve)
			if statusErr != nil {
				return webaccess.Outcome{Error: statusErr.Error(), Warnings: warnings, Observed: &m}
			}
			if !status.Online {
				break
			}
			if !time.Now().Before(deadline) {
				return webaccess.Outcome{Error: "Agent runtime did not stop before shelving.", Warnings: warnings, Observed: &m}
			}
			select {
			case <-ctx.Done():
				return webaccess.Outcome{Error: ctx.Err().Error(), Warnings: warnings, Observed: &m}
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	result, err := b.cfg.App.Shelve(ctx, resolve, false)
	warnings = append(warnings, result.Warnings...)
	out := webaccess.Outcome{Result: result, Warnings: warnings}
	if err != nil {
		out.Error = err.Error()
	} else {
		out.Observed = &result.Manifest
	}
	return out
}

func (b webBackend) Archive(ctx context.Context, id string) webaccess.Outcome {
	owner, err := b.cfg.App.AgentRuntimeStatus(ctx, b.resolve(id))
	if err != nil {
		return webaccess.Outcome{Error: err.Error()}
	}
	if owner.Online {
		content, contentErr := b.cfg.App.AgentContent(ctx, b.resolve(id))
		if contentErr != nil || !content.IdleGuard {
			return webaccess.Outcome{Error: "This existing owner lacks the new content/idle-guard protocol. Stop it explicitly through the CLI after it settles before archiving from the browser."}
		}
	}
	result, err := b.cfg.App.Archive(ctx, b.resolve(id), false)
	out := webaccess.Outcome{Result: result, Warnings: result.Warnings}
	if err != nil {
		out.Error = err.Error()
		if len(result.Warnings) > 0 {
			out.Warnings = append(out.Warnings, "Archive did not complete. Retained successful steps are not rolled back.")
		}
	}
	inspect, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	m, readErr := b.cfg.App.ResolveItem(inspect, b.resolve(id))
	if readErr == nil {
		out.Observed = &m
		if err != nil {
			out.Warnings = append(out.Warnings, "Observed lifecycle after attempt: "+m.State)
		}
	} else {
		out.Warnings = append(out.Warnings, "Could not refresh durable state: "+strings.TrimSpace(readErr.Error()))
	}
	return out
}
