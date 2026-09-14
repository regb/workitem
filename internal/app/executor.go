package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	executorprotocol "github.com/regb/workitem/executor/protocol"
	"github.com/regb/workitem/internal/coordinator"
	executorclient "github.com/regb/workitem/internal/executor"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/version"
)

type ExecutorClient interface {
	Doctor(context.Context) (executorprotocol.DoctorResult, error)
	EnsureAuthority(context.Context, string) error
	EnsureWorkspace(context.Context, model.Manifest, string, string, string, string) (executorprotocol.WorkspaceResult, error)
	WorkspaceStatus(context.Context, string) (executorprotocol.WorkspaceResult, error)
	ReleaseWorkspace(context.Context, model.Manifest, bool) (executorprotocol.WorkspaceResult, error)
	ImportWorkspaceBundle(context.Context, model.Manifest, []byte) error
	EnsureRuntime(context.Context, string, string) (executorprotocol.RuntimeResult, error)
	RuntimeStatus(context.Context, string) (executorprotocol.RuntimeResult, error)
	RuntimeControl(context.Context, string, string, string) (executorprotocol.RuntimeResult, error)
	CloseTerminal(context.Context, string) error
	Shutdown(context.Context) error
	TerminalCommands(string) (agent, shell []string, err error)
}

type ExecutorInfo struct {
	Name        string `json:"name"`
	Destination string `json:"destination"`
}

type ExecutorAssignmentResult struct {
	WorkItemID string         `json:"work_item_id"`
	Previous   string         `json:"previous"`
	Executor   string         `json:"executor"`
	Changed    bool           `json:"changed"`
	Manifest   model.Manifest `json:"manifest"`
}

type ExecutorDoctorResult struct {
	Executor     ExecutorInfo                  `json:"executor"`
	LocalVersion string                        `json:"local_version"`
	Compatible   bool                          `json:"compatible"`
	Worker       executorprotocol.DoctorResult `json:"worker"`
	Warnings     []string                      `json:"warnings"`
}

func (a *App) AssignExecutor(ctx context.Context, opts ResolveOptions, name string) (ExecutorAssignmentResult, error) {
	manifest, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return ExecutorAssignmentResult{}, err
	}
	name, err = a.resolveExecutorName(name, false)
	if err != nil {
		return ExecutorAssignmentResult{}, err
	}
	result := ExecutorAssignmentResult{WorkItemID: manifest.ID, Previous: manifest.Checkout.Executor, Executor: name, Manifest: manifest}
	if manifest.Checkout.Kind != model.WorkspaceKindManagedSlot {
		return result, fmt.Errorf("repository-home checkout cannot select an executor")
	}
	if manifest.Checkout.Present() {
		return result, fmt.Errorf("release the current workspace before assigning an executor")
	}
	if manifest.PrimaryConversation != nil && manifest.PrimaryConversation.Executor != "" && manifest.PrimaryConversation.Executor != name {
		return result, fmt.Errorf("cannot move an existing native conversation between executors")
	}
	if runtime, loadErr := a.Store.LoadAgentRuntime(manifest.ID); loadErr != nil {
		return result, loadErr
	} else if runtime != nil && runtime.State != model.AgentRuntimeStopped {
		return result, fmt.Errorf("stop the active agent runtime before assigning an executor")
	}
	previous := manifest.Checkout.Executor
	if previous == "" {
		previous = model.ExecutorLocal
	}
	if previous == name {
		result.Previous, result.Executor, result.Manifest = previous, name, manifest
		return result, nil
	}
	manifest.Checkout.Executor, manifest.Checkout.Slot = name, ""
	manifest.UpdatedAt = a.now()
	if err := a.Store.SaveManifest(ctx, manifest); err != nil {
		return ExecutorAssignmentResult{}, err
	}
	_ = a.Store.AppendEvent(ctx, manifest.ID, model.NewEvent(a.now(), "checkout.executor_assigned", "user", map[string]any{"previous_executor": previous, "executor": name}))
	result.Previous, result.Executor, result.Changed, result.Manifest = previous, name, true, manifest
	return result, nil
}

func (a *App) Executors() []ExecutorInfo {
	result := make([]ExecutorInfo, 0, len(a.ExecutorsConfig))
	for name, definition := range a.ExecutorsConfig {
		result = append(result, ExecutorInfo{Name: name, Destination: definition.Destination})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (a *App) executorClient(name string) (ExecutorClient, error) {
	definition, ok := a.ExecutorsConfig[name]
	if !ok {
		return nil, fmt.Errorf("executor %q is not configured", name)
	}
	if client := a.ExecutorClients[name]; client != nil {
		return client, nil
	}
	return executorclient.Client{Name: name, Definition: definition, MasterID: a.MasterID, Environment: a.Environment}, nil
}

func (a *App) ensureRemoteWorkspace(ctx context.Context, manifest model.Manifest) (model.Manifest, []string, error) {
	executorName := manifest.Checkout.Executor
	client, err := a.executorClient(executorName)
	if err != nil {
		return model.Manifest{}, nil, err
	}
	doctor, err := client.Doctor(ctx)
	if err != nil {
		return model.Manifest{}, nil, err
	}
	if err := client.EnsureAuthority(ctx, a.ExecutorDaemonSocketPath); err != nil {
		return model.Manifest{}, nil, err
	}
	if doctor.Version != version.Current().Version {
		return model.Manifest{}, nil, fmt.Errorf("executor %s wi version %s does not match coordinator version %s", executorName, doctor.Version, version.Current().Version)
	}
	requiredTools := []string{"git", "tmux", "bash"}
	if definition := a.AgentsConfig[manifest.Agent]; definition.Driver == "pi" {
		requiredTools = append(requiredTools, "pi")
	}
	for _, tool := range requiredTools {
		if doctor.Tools[tool] == "missing" {
			return model.Manifest{}, nil, fmt.Errorf("executor %s is missing required tool %s", executorName, tool)
		}
	}
	repositoryKey := executorRepositoryKey(manifest.Repository)
	slot, err := a.remoteSlot(manifest.ID, executorName, repositoryKey, manifest.Checkout.Slot)
	if err != nil {
		return model.Manifest{}, nil, err
	}
	description, err := os.ReadFile(filepath.Join(a.Store.ItemDir(manifest.ID), model.DescriptionFilename))
	if err != nil && !os.IsNotExist(err) {
		return model.Manifest{}, nil, err
	}
	configText, err := a.executorAgentConfig(executorName, manifest.Agent)
	if err != nil {
		return model.Manifest{}, nil, err
	}
	result, err := client.EnsureWorkspace(ctx, manifest, string(description), repositoryKey, slot, configText)
	if err != nil {
		return model.Manifest{}, nil, err
	}
	manifest.Checkout.Path = &result.Path
	manifest.Checkout.Slot = result.Slot
	manifest.UpdatedAt = a.now()
	if err := a.Store.SaveManifest(ctx, manifest); err != nil {
		return model.Manifest{}, nil, err
	}
	_ = a.Store.AppendEvent(ctx, manifest.ID, model.NewEvent(a.now(), "checkout.assigned", "wi", map[string]any{"executor": executorName, "slot": result.Slot, "path": result.Path, "source": result.Source}))
	return manifest, nil, nil
}

func (a *App) ensureRemoteRuntime(ctx context.Context, manifest model.Manifest, mode string) (EnsureAgentRuntimeResult, error) {
	client, err := a.executorClient(manifest.Checkout.Executor)
	if err != nil {
		return EnsureAgentRuntimeResult{}, err
	}
	if err := client.EnsureAuthority(ctx, a.ExecutorDaemonSocketPath); err != nil {
		return EnsureAgentRuntimeResult{}, err
	}
	result, err := client.EnsureRuntime(ctx, manifest.ID, mode)
	if err != nil {
		return EnsureAgentRuntimeResult{}, err
	}
	var runtime model.AgentRuntime
	if err := json.Unmarshal(result.RuntimeJSON, &runtime); err != nil {
		return EnsureAgentRuntimeResult{}, fmt.Errorf("decode executor runtime: %w", err)
	}
	var remoteManifest model.Manifest
	if err := json.Unmarshal(result.ManifestJSON, &remoteManifest); err != nil {
		return EnsureAgentRuntimeResult{}, fmt.Errorf("decode executor manifest: %w", err)
	}
	runtime.Executor = manifest.Checkout.Executor
	runtime.HostPID, runtime.HostProcessGroup, runtime.HostStartTime = 0, 0, 0
	if err := a.Store.SaveAgentRuntime(ctx, manifest.ID, runtime); err != nil {
		return EnsureAgentRuntimeResult{}, err
	}
	warnings := a.ingestExecutorEvents(ctx, result.EventsJSON)
	if remoteManifest.PrimaryConversation != nil {
		conversation := *remoteManifest.PrimaryConversation
		conversation.Executor = manifest.Checkout.Executor
		manifest.PrimaryConversation = &conversation
		manifest.UpdatedAt = a.now()
		if err := a.Store.SaveManifest(ctx, manifest); err != nil {
			return EnsureAgentRuntimeResult{}, err
		}
	}
	if manifest.PrimaryConversation == nil {
		return EnsureAgentRuntimeResult{}, fmt.Errorf("executor started a runtime without a primary conversation")
	}
	if err := a.checkpointExecutorConversation(ctx, manifest, result.Conversation); err != nil {
		warnings = append(warnings, "could not checkpoint executor conversation: "+err.Error())
	}
	resultValue := EnsureAgentRuntimeResult{WorkItemID: manifest.ID, Created: result.Changed, Runtime: runtime, Conversation: *manifest.PrimaryConversation, Capabilities: runtime.Capabilities, Warnings: warnings}
	if mode == "tui" {
		resultValue.Terminal = &TerminalResult{WorkItemID: manifest.ID, Session: manifest.TerminalSessionName(), Checkout: manifest.Checkout}
	}
	return resultValue, nil
}

func (a *App) checkpointExecutorConversation(ctx context.Context, manifest model.Manifest, snapshot *executorprotocol.ConversationSnapshot) error {
	if snapshot == nil || manifest.PrimaryConversation == nil {
		return nil
	}
	conversation := *manifest.PrimaryConversation
	if snapshot.ID != conversation.ID || snapshot.Driver != conversation.Driver || snapshot.Locator != conversation.NativeLocator() {
		return fmt.Errorf("executor conversation snapshot identity does not match the primary conversation")
	}
	backend := a.ConversationBackends[conversation.Driver]
	importer, ok := backend.(interface {
		ImportConversation(context.Context, string, []byte) error
	})
	if !ok {
		return fmt.Errorf("conversation driver %q does not support snapshots", conversation.Driver)
	}
	path, err := a.primaryAgentService().AbsPath(manifest.ID, conversation.NativeLocator())
	if err != nil {
		return err
	}
	return importer.ImportConversation(ctx, path, snapshot.Data)
}

func executorEventStatus(raw []byte) (string, string) {
	var events coordinator.RuntimeEventsResult
	if len(raw) == 0 || json.Unmarshal(raw, &events) != nil || len(events.Events) == 0 {
		return "", ""
	}
	event := events.Events[len(events.Events)-1]
	switch event.Type {
	case "runtime.ready", "agent.settled", "turn.ended":
		return "idle", "latest executor runtime event reports settled work"
	case "agent.started", "turn.started", "tool.started", "command.accepted":
		return "busy", "latest executor runtime event reports active work"
	case "runtime.failed", "command.rejected":
		return "problem", "latest executor runtime event reports a failure"
	default:
		return "", ""
	}
}

func (a *App) ingestExecutorEvents(ctx context.Context, raw []byte) []string {
	if len(raw) == 0 || strings.TrimSpace(a.DaemonSocketPath) == "" {
		return nil
	}
	var events coordinator.RuntimeEventsResult
	if err := json.Unmarshal(raw, &events); err != nil {
		return []string{"could not decode executor runtime events: " + err.Error()}
	}
	client := &coordinator.Client{SocketPath: a.DaemonSocketPath}
	warnings := []string{}
	for _, event := range events.Events {
		payload := struct {
			RuntimeID string `json:"runtime_id"`
			CommandID string `json:"command_id"`
			RequestID string `json:"request_id"`
			Role      string `json:"role"`
			ToolName  string `json:"tool_name"`
			Failed    bool   `json:"failed"`
		}{}
		_ = json.Unmarshal(event.Payload, &payload)
		_, err := client.IngestRuntimeEvent(ctx, coordinator.RuntimeSemanticEvent{ID: event.ID, RuntimeID: payload.RuntimeID, WorkItemID: event.ItemID, Type: event.Type, Timestamp: event.Timestamp, CommandID: payload.CommandID, RequestID: payload.RequestID, Role: payload.Role, ToolName: payload.ToolName, Failed: payload.Failed})
		if err != nil {
			warnings = append(warnings, "could not ingest executor event "+event.ID+": "+err.Error())
		}
	}
	return warnings
}

func (a *App) remoteSlot(itemID, executorName, repositoryKey, existing string) (string, error) {
	if strings.TrimSpace(existing) != "" {
		return existing, nil
	}
	items, errs := a.Store.ListManifests()
	if len(errs) > 0 {
		return "", errs[0]
	}
	used := map[int]bool{}
	for _, item := range items {
		if item.ID == itemID || item.Checkout.Executor != executorName || executorRepositoryKey(item.Repository) != repositoryKey {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimPrefix(item.Checkout.Slot, "slot-")); err == nil && n > 0 {
			used[n] = true
		}
	}
	for index := 1; ; index++ {
		if !used[index] {
			return fmt.Sprintf("slot-%04d", index), nil
		}
	}
}

func executorRepositoryKey(repository model.Repository) string {
	value := strings.TrimSpace(repository.GitCommonDir)
	if value == "" {
		value = strings.TrimSpace(repository.RemoteURL)
	}
	if value == "" {
		value = strings.TrimSpace(repository.RootAtCreation)
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func (a *App) executorAgentConfig(executorName, agentName string) (string, error) {
	if !executorConfigIdentifier(agentName) {
		return "", fmt.Errorf("agent name %q cannot be forwarded to an executor", agentName)
	}
	definition, ok := a.AgentsConfig[agentName]
	if !ok {
		return "", fmt.Errorf("agent definition %q is not configured", agentName)
	}
	if !executorConfigIdentifier(definition.Driver) {
		return "", fmt.Errorf("driver name %q cannot be forwarded to an executor", definition.Driver)
	}
	driver, ok := a.DriversConfig[definition.Driver]
	if !ok {
		return "", fmt.Errorf("agent driver %q is not configured", definition.Driver)
	}
	merged := map[string]any{}
	for key, value := range driver.Defaults {
		merged[key] = value
	}
	for key, value := range definition.Config {
		merged[key] = value
	}
	// Pi's current options are host-local paths. They must be selected by the
	// executor override rather than copied from the coordinator machine.
	if definition.Driver == "pi" {
		delete(merged, "executable")
		delete(merged, "extensions")
	}
	if executor, ok := a.ExecutorsConfig[executorName]; ok {
		for key, value := range executor.Agents[agentName].Config {
			merged[key] = value
		}
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "[agents.default]\ndriver = %q\n\n", definition.Driver)
	if agentName != "default" {
		fmt.Fprintf(&builder, "[agents.%s]\ndriver = %q\n\n", agentName, definition.Driver)
	}
	fmt.Fprintf(&builder, "[agents.%s.config]\n", agentName)
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !executorConfigIdentifier(key) {
			return "", fmt.Errorf("agent config key %q cannot be forwarded to an executor", key)
		}
		value, err := executorConfigValue(merged[key])
		if err != nil {
			return "", fmt.Errorf("forward agent config %s: %w", key, err)
		}
		fmt.Fprintf(&builder, "%s = %s\n", key, value)
	}
	fmt.Fprintf(&builder, "\n[drivers.%s]\n", definition.Driver)
	return builder.String(), nil
}

func executorConfigIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func executorConfigValue(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return fmt.Sprintf("%q", typed), nil
	case bool:
		return strconv.FormatBool(typed), nil
	case int:
		return strconv.Itoa(typed), nil
	case []string:
		parts := make([]string, len(typed))
		for i, item := range typed {
			parts[i] = fmt.Sprintf("%q", item)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case []any:
		parts := make([]string, len(typed))
		for i, item := range typed {
			text, ok := item.(string)
			if !ok {
				return "", fmt.Errorf("arrays must contain strings")
			}
			parts[i] = fmt.Sprintf("%q", text)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	default:
		return "", fmt.Errorf("unsupported value type %T", value)
	}
}

func (a *App) ExecutorShutdown(ctx context.Context, name string) error {
	client, err := a.executorClient(strings.TrimSpace(name))
	if err != nil {
		return err
	}
	return client.Shutdown(ctx)
}

func (a *App) ExecutorDoctor(ctx context.Context, name string) (ExecutorDoctorResult, error) {
	name = strings.TrimSpace(name)
	definition, ok := a.ExecutorsConfig[name]
	if !ok {
		return ExecutorDoctorResult{}, fmt.Errorf("executor %q is not configured", name)
	}
	client, err := a.executorClient(name)
	if err != nil {
		return ExecutorDoctorResult{}, err
	}
	worker, err := client.Doctor(ctx)
	if err != nil {
		return ExecutorDoctorResult{}, fmt.Errorf("inspect executor %s: %w", name, err)
	}
	localVersion := version.Current().Version
	result := ExecutorDoctorResult{Executor: ExecutorInfo{Name: name, Destination: definition.Destination}, LocalVersion: localVersion, Compatible: worker.Version == localVersion, Worker: worker, Warnings: []string{}}
	if !result.Compatible {
		result.Warnings = append(result.Warnings, fmt.Sprintf("worker wi version %s does not match coordinator version %s", worker.Version, localVersion))
	}
	for tool, value := range worker.Tools {
		if value == "missing" {
			result.Warnings = append(result.Warnings, "worker tool "+tool+" is missing")
		}
	}
	sort.Strings(result.Warnings)
	return result, nil
}
