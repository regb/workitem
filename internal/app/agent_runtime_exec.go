package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
	agentcore "github.com/regb/workitem/internal/app/core/primaryagent"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/skills"
)

func (a *App) AgentExec(ctx context.Context, itemID, conversationID, runtimeID string, mode agent.Mode) error {
	manifest, err := a.Store.LoadManifest(itemID)
	if err != nil {
		return err
	}
	agentName := manifest.Agent
	if agentName == "" {
		agentName = "default"
	}
	definition, ok := a.AgentsConfig[agentName]
	if !ok {
		return fmt.Errorf("agent definition %q is not configured", agentName)
	}
	driverDefinition, ok := a.DriversConfig[definition.Driver]
	if !ok {
		return fmt.Errorf("agent driver %q is not configured", definition.Driver)
	}
	configJSON, err := a.driverConfigJSON(definition)
	if err != nil {
		return fmt.Errorf("encode agent driver configuration: %w", err)
	}
	driverPath := driverDefinition.Command
	var driverArgs []string
	if driverPath == "" {
		bundled := a.BundledDriverCommands[definition.Driver]
		driverPath, driverArgs = bundled.Path, append([]string(nil), bundled.Args...)
	}
	if driverPath == "" {
		return fmt.Errorf("agent driver %q has no executable", definition.Driver)
	}
	if a.RunAgentDriver == nil {
		return fmt.Errorf("agent driver runner is not configured")
	}
	return a.primaryAgentService().Execute(ctx, itemID, conversationID, runtimeID, mode, func(ctx context.Context, spec agentcore.ExecutionSpec) error {
		events, err := driverprotocol.ListenEvents(spec.EventSocketPath)
		if err != nil {
			return fmt.Errorf("listen for agent driver events: %w", err)
		}
		relayDone := make(chan struct{})
		go func() {
			defer close(relayDone)
			client := &coordinator.Client{SocketPath: a.DaemonSocketPath}
			for request := range events.Requests() {
				event := request.Event
				if event.ProtocolVersion != driverprotocol.RuntimeVersion || event.RuntimeID != spec.RuntimeID || event.WorkItemID != spec.WorkItemID {
					request.Respond(fmt.Errorf("runtime event identity does not match its launch"))
					continue
				}
				_, err := client.IngestRuntimeEvent(ctx, coordinator.RuntimeSemanticEvent{ID: event.EventID, RuntimeID: event.RuntimeID, WorkItemID: event.WorkItemID, Type: event.Type, Timestamp: event.Timestamp, CommandID: event.CommandID, RequestID: event.RequestID, Role: event.Role, ToolName: event.ToolName, Failed: event.Failed})
				request.Respond(err)
			}
		}()
		environment := map[string]string{"WI_ID": spec.WorkItemID, "WI_DIR": a.Store.ItemDir(spec.WorkItemID), "WI_WORKTREE": spec.CWD, "WI_AGENT_RUNTIME_ID": spec.RuntimeID, "WI_AGENT_MODE": string(spec.Mode), "WI_AGENT_CONTROL_SOCKET": spec.ControlSocketPath, "WI_AGENT_EVENT_SOCKET": spec.EventSocketPath, "WI_AGENT_CONVERSATION_DIR": spec.ConversationDir}
		format := ""
		if manifest.PrimaryConversation != nil {
			format = manifest.PrimaryConversation.Format
		}
		request := driverprotocol.LaunchRequest{ProtocolVersion: driverprotocol.Version, Driver: definition.Driver, Agent: agentName, Config: configJSON, Skills: skills.Bundled(), Runtime: driverprotocol.Runtime{ID: spec.RuntimeID, WorkItemID: spec.WorkItemID, Mode: driverprotocol.Mode(spec.Mode), CWD: spec.CWD, ControlSocketPath: spec.ControlSocketPath, EventSocketPath: spec.EventSocketPath, LogPath: spec.LogPath}, Conversation: driverprotocol.Conversation{ID: conversationID, Locator: spec.ConversationLocator, Format: format}, Environment: environment}
		runErr := a.RunAgentDriver(ctx, driverPath, driverArgs, request, driverProcessEnvironment(os.Environ()), func(ready driverprotocol.Ready) error {
			runtime, err := a.Store.LoadAgentRuntime(spec.WorkItemID)
			if err != nil {
				return err
			}
			if runtime == nil || runtime.ID != spec.RuntimeID {
				return fmt.Errorf("agent runtime ownership changed during driver startup")
			}
			runtime.Capabilities = model.RuntimeCapabilities{LiveContent: ready.Capabilities.LiveContent, IdleGuard: ready.Capabilities.IdleGuard, ImageInput: ready.Capabilities.ImageInput, NativeTUI: ready.Capabilities.NativeTUI, Headless: ready.Capabilities.Headless, Streaming: ready.Capabilities.Streaming, Prompt: ready.Capabilities.Prompt, Steering: ready.Capabilities.Steering, FollowUp: ready.Capabilities.FollowUp, Cancel: ready.Capabilities.Cancel, Monitor: ready.Capabilities.Monitor}
			runtime.UpdatedAt = a.now()
			return a.Store.SaveAgentRuntime(ctx, spec.WorkItemID, *runtime)
		})
		_ = events.Close()
		<-relayDone
		return runErr
	})
}

func driverProcessEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "WI_DAEMON_SOCKET", "WI_DAEMON_PROTOCOL", "WI_BUILD_IDENTITY":
			continue
		}
		result = append(result, entry)
	}
	return result
}
