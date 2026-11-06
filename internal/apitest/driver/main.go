// Command driver is a deterministic coding-agent driver for isolated API tests.
// It never calls a model or reads credentials.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	p "github.com/regb/workitem/agentdriver/protocol"
	pi "github.com/regb/workitem/internal/agentdriver/pi"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context) error {
	request, reply, err := p.ConnectRequest(ctx, os.Getenv(p.BootstrapSocketEnv), p.Hello{ProtocolVersion: p.Version, Driver: "pi", DriverVersion: "api-test"})
	if err != nil {
		return err
	}
	response := p.Response{ProtocolVersion: p.Version}
	fail := func(err error) error { response.Error = err.Error(); _ = reply(response); return err }
	switch request.Operation {
	case p.OperationConfigDiscover:
		response.ConfigDiscovery = &p.ConfigDiscoveryResult{Profiles: []p.DiscoveredAgentConfig{{Name: "pi", Label: "Test agent", Config: json.RawMessage(`{}`)}}}
	case p.OperationConfigValidate:
		var c map[string]any
		_ = json.Unmarshal(request.Config, &c)
		if reason, ok := c["reasoning"].(string); ok && reason != "" && !slices.Contains(p.ReasoningLevels, reason) {
			return fail(fmt.Errorf("invalid reasoning level %q", reason))
		}
		response.ConfigValidation = &p.ConfigValidationResult{Valid: true}
	case p.OperationRuntimeModes:
		modes := pi.RuntimeModes()
		response.RuntimeModes = &modes
	case p.OperationConfigModels:
		response.ModelCatalog = &p.ModelCatalog{Models: []p.ModelOption{{Value: "test/model", Label: "Test model", Reasoning: true, Levels: p.ReasoningLevels, ImageInput: true}}, ReasoningLevels: p.ReasoningLevels, Default: "test/model", DefaultReasoning: "medium"}
	case p.OperationLaunch:
		if request.Launch == nil {
			return fail(fmt.Errorf("missing launch"))
		}
		return launch(ctx, *request.Launch, reply)
	default:
		r := request.Conversation
		if r == nil {
			return fail(fmt.Errorf("missing conversation request"))
		}
		out := &p.ConversationResponse{}
		response.Conversation = out
		switch request.Operation {
		case p.OperationConversationCreate:
			c := pi.New("pi").NewConversation(r.ID)
			out.Conversation = p.Conversation{ID: c.ID, Locator: c.NativeLocator(), Format: c.Format}
		case p.OperationConversationCapabilities:
			out.Capabilities = &p.ConversationCapabilities{History: true, HistoryPaging: true, Title: true}
		case p.OperationConversationTitle:
			out.Title = "Test suggested title"
		case p.OperationConversationCWD:
			out.CWD, err = pi.New("pi").ConversationCWD(r.Path)
		case p.OperationConversationFork:
			err = pi.New("pi").ForkConversation(ctx, r.SourcePath, r.TargetPath, r.TargetCWD)
		case p.OperationConversationHistory:
			h, e := pi.ReadHistoryPage(ctx, r.Path, r.LeafID, r.BeforeID)
			out.History = &h
			err = e
		case p.OperationConversationObserve:
			observation, warnings := pi.ObserveConversationPath(r.Path, r.Now)
			out.Observation = &observation
			out.Warnings = warnings
		case p.OperationConversationDecode:
			out.Decoded = make([]bool, len(r.Lines))
			for i, line := range r.Lines {
				number := r.StartLine + uint64(i)
				if len(r.LineNumbers) == len(r.Lines) {
					number = r.LineNumbers[i]
				}
				fact, ok := pi.SummarizeConversationFact(number, line)
				out.Decoded[i] = ok
				if ok {
					out.Facts = append(out.Facts, fact)
				}
			}
		default:
			err = fmt.Errorf("unsupported operation %s", request.Operation)
		}
		if err != nil {
			return fail(err)
		}
	}
	return reply(response)
}
func launch(ctx context.Context, r p.LaunchRequest, reply func(p.Response) error) error {
	if err := os.MkdirAll(filepath.Dir(r.Conversation.Locator), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(r.Conversation.Locator, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	info, _ := file.Stat()
	if info.Size() == 0 {
		_ = json.NewEncoder(file).Encode(map[string]any{"type": "session", "version": 3, "id": r.Conversation.ID, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "cwd": r.Runtime.CWD})
	}
	control, err := p.ListenControl(r.Runtime.ControlSocketPath)
	if err != nil {
		return err
	}
	defer control.Close()
	content, err := p.ListenContent(p.ContentSocketPath(r.Runtime.ControlSocketPath), r.Runtime.ID, r.Runtime.WorkItemID, true)
	if err != nil {
		return err
	}
	defer content.Close()
	emit := func(event p.RuntimeEvent) error {
		event.ProtocolVersion = p.RuntimeVersion
		event.RuntimeID = r.Runtime.ID
		event.WorkItemID = r.Runtime.WorkItemID
		event.Timestamp = time.Now().UTC()
		event.EventID = fmt.Sprintf("fake-%d", time.Now().UnixNano())
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return p.SubmitEvent(ctx, r.Runtime.EventSocketPath, event)
	}
	var settings map[string]any
	_ = json.Unmarshal(r.Config, &settings)
	caps := p.Capabilities{LiveContent: true, IdleGuard: true, ImageInput: true, BundledSkills: true, NativeTUI: r.Runtime.Mode == p.ModeTUI, Headless: r.Runtime.Mode == p.ModeHeadless, Streaming: true, Prompt: true, Steering: true, FollowUp: true, Cancel: true, Monitor: true, Control: p.ControlStructured, Settings: []string{"model", "reasoning"}}
	if emulated, _ := settings["emulated_tui"].(bool); emulated && r.Runtime.Mode == p.ModeTUI {
		caps.Control = p.ControlEmulated
		caps.Prompt = false
		caps.Steering = false
		caps.FollowUp = false
		caps.Cancel = false
		caps.Settings = nil
	}
	if err := reply(p.Response{ProtocolVersion: p.Version, Ready: &p.Ready{ProtocolVersion: p.Version, Capabilities: caps}}); err != nil {
		return err
	}
	if err := emit(p.RuntimeEvent{Type: p.EventRuntimeReady}); err != nil {
		return err
	}
	modelName, _ := settings["model"].(string)
	if modelName == "" {
		modelName = "test/model"
	}
	_ = emit(p.RuntimeEvent{Type: p.EventSettingsChanged, Settings: map[string]string{"model": modelName, "reasoning": "medium"}})
	var mu sync.Mutex
	busy := false
	leaf := "-"
	seen := map[string]bool{}
	counter := 0
	appendMessage := func(role, text string) string {
		counter++
		entry := fmt.Sprintf("fake%08d", counter)
		var parent any
		if leaf != "-" {
			parent = leaf
		}
		message := map[string]any{"role": role, "content": []map[string]string{{"type": "text", "text": text}}}
		if role == "assistant" {
			message["stopReason"] = "stop"
		}
		_ = json.NewEncoder(file).Encode(map[string]any{"type": "message", "id": entry, "parentId": parent, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "message": message})
		_ = file.Sync()
		leaf = entry
		return entry
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case request, ok := <-control.Requests():
			if !ok {
				return nil
			}
			c := request.Command
			mu.Lock()
			if c.RequireIdle && busy {
				mu.Unlock()
				request.Respond(p.NotIdle("test agent is busy"))
				continue
			}
			if c.RequestID != "" && seen[c.RequestID] {
				mu.Unlock()
				request.Respond(nil)
				continue
			}
			if c.RequestID != "" {
				seen[c.RequestID] = true
			}
			mu.Unlock()
			request.Respond(nil)
			reason := ""
			if c.Type == p.CommandSet {
				reason = p.ReasonSettingApplied
			}
			if c.Type == p.CommandAbort {
				reason = p.ReasonAbortAccepted
			}
			_ = emit(p.RuntimeEvent{Type: p.EventCommandAccepted, CommandID: c.ID, RequestID: c.RequestID, ReasonCode: reason})
			switch c.Type {
			case p.CommandShutdown:
				return nil
			case p.CommandSet:
				if c.Setting != nil {
					_ = emit(p.RuntimeEvent{Type: p.EventSettingsChanged, Settings: map[string]string{c.Setting.Name: c.Setting.Value}})
				}
			case p.CommandAbort:
				mu.Lock()
				busy = false
				current := leaf
				mu.Unlock()
				content.Update(p.EventMessageCompleted, "", "")
				content.SetActivity("Idle")
				_ = emit(p.RuntimeEvent{Type: p.EventAgentSettled, Outcome: "completed", Readiness: "ready"})
				_ = emit(p.RuntimeEvent{Type: p.EventRuntimeStatus, Readiness: "ready", AgentState: "idle", LeafID: current})
			case p.CommandPrompt, p.CommandSteer, p.CommandFollowUp:
				mu.Lock()
				busy = true
				user := appendMessage("user", c.Message)
				current := leaf
				mu.Unlock()
				content.PromptSaved(c.RequestID, []string{user})
				content.StartMessage()
				content.SetActivity("Thinking")
				_ = emit(p.RuntimeEvent{Type: p.EventAgentStarted, Readiness: "busy"})
				_ = emit(p.RuntimeEvent{Type: p.EventRuntimeStatus, Readiness: "busy", AgentState: "active", LeafID: current})
				if strings.Contains(c.Message, "hold") {
					continue
				}
				time.Sleep(30 * time.Millisecond)
				answer := "Echo: " + c.Message
				content.Update(p.EventMessageDelta, answer, "")
				mu.Lock()
				assistant := appendMessage("assistant", answer)
				busy = false
				current = leaf
				mu.Unlock()
				content.MarkSaved(answer, []string{assistant})
				content.SetActivity("Idle")
				_ = emit(p.RuntimeEvent{Type: p.EventAgentSettled, Outcome: "completed", Readiness: "ready"})
				_ = emit(p.RuntimeEvent{Type: p.EventRuntimeStatus, Readiness: "ready", AgentState: "idle", LeafID: current})
			}
		}
	}
}
