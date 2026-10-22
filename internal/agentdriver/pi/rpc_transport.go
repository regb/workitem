package pi

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
)

const rpcReadyCommandID = "wi-runtime-ready"

type rpcOutput struct {
	Type       string          `json:"type"`
	ID         string          `json:"id,omitempty"`
	Command    string          `json:"command,omitempty"`
	Success    bool            `json:"success,omitempty"`
	Error      string          `json:"error,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	RuntimeID  string          `json:"runtime_id,omitempty"`
	WorkItemID string          `json:"work_item_id,omitempty"`
	AgentState string          `json:"agent_state,omitempty"`
	LeafID     string          `json:"leaf_id,omitempty"`
	Readiness  string          `json:"readiness,omitempty"`
}

type queuedLiveEvent struct {
	event driverprotocol.RuntimeEvent
	ack   chan struct{}
}

type nativeRPCTransport struct {
	content             *driverprotocol.ContentServer
	spec                ExecSpec
	stdin               io.WriteCloser
	pending             map[string]agent.ControlCommand
	handled             map[string]bool
	lastAssistantFailed bool
	// unsaved are messages whose session entries are not yet known; Pi appends
	// an entry after dispatching its message_end.
	unsaved []unsavedMessage
	// prompts are submitted prompts with a request ID, oldest first, waiting
	// for the user message that carries them.
	prompts          []submittedPrompt
	busy             bool
	work             map[string]bool // Submitted work, with true marking settlement before its response.
	responses        map[string]func(error)
	shutdownPending  bool
	ready            bool
	stopping         bool
	currentRequestID string
	liveQueue        chan queuedLiveEvent
	liveContext      context.Context
	liveNonce        string
	liveSequence     uint64
	liveDisabled     atomic.Bool
}

func runNativeRPC(ctx context.Context, path string, args, env []string, spec ExecSpec) error {
	if strings.TrimSpace(spec.RuntimeID) == "" || strings.TrimSpace(spec.WorkItemID) == "" {
		return fmt.Errorf("native Pi RPC requires runtime and work-item IDs")
	}
	if strings.TrimSpace(spec.ControlSocketPath) == "" {
		return fmt.Errorf("native Pi RPC requires a control socket path")
	}
	if strings.TrimSpace(spec.EventSocketPath) == "" {
		return fmt.Errorf("native Pi headless mode requires an event socket path")
	}

	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = spec.CWD
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var log *os.File
	if strings.TrimSpace(spec.LogPath) != "" {
		if err := os.MkdirAll(filepath.Dir(spec.LogPath), 0o700); err != nil {
			return err
		}
		log, err = os.OpenFile(spec.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer log.Close()
		cmd.Stderr = log
	} else {
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	nonceBytes := make([]byte, 12)
	_, _ = rand.Read(nonceBytes)
	transport := &nativeRPCTransport{
		spec:      spec,
		stdin:     stdin,
		pending:   map[string]agent.ControlCommand{},
		handled:   map[string]bool{},
		work:      map[string]bool{},
		responses: map[string]func(error){},
		liveNonce: hex.EncodeToString(nonceBytes),
	}
	content, err := driverprotocol.ListenContent(driverprotocol.ContentSocketPath(spec.ControlSocketPath), spec.RuntimeID, spec.WorkItemID, true)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("listen for Pi content: %w", err)
	}
	defer content.Close()
	transport.content = content
	transport.liveQueue = make(chan queuedLiveEvent, 256)
	transport.liveContext = ctx
	go transport.runLiveSender(ctx)
	control, err := agent.ListenControlSocket(spec.ControlSocketPath)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("listen for Pi RPC control: %w", err)
	}
	defer control.Close()
	if err := transport.writeNativeCommand(map[string]any{"id": rpcReadyCommandID, "type": "get_commands"}); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}

	lines := make(chan []byte, 64)
	scanErr := make(chan error, 1)
	go scanRPCOutput(stdout, lines, scanErr, log)
	processDone := make(chan error, 1)
	go func() { processDone <- cmd.Wait() }()
	defer stdin.Close()
	var processExit error
	processExited := false
	waitingForReady := []agent.ControlSocketRequest{}
	defer func() {
		for _, request := range waitingForReady {
			request.Respond(fmt.Errorf("RPC runtime stopped before becoming ready"))
		}
	}()

	for {
		// Consume output already written before process exit. A guarded shutdown
		// result must reach its control client before the socket is closed.
		if processExited && lines == nil {
			if transport.stopping && isExpectedProcessExit(processExit) {
				return nil
			}
			return processExit
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-processDone:
			processExit, processExited = err, true
			processDone = nil
		case err := <-scanErr:
			if err != nil && !(transport.stopping && errors.Is(err, os.ErrClosed)) {
				return fmt.Errorf("read Pi RPC output: %w", err)
			}
			scanErr = nil
		case line, ok := <-lines:
			if !ok {
				lines = nil
				continue
			}
			if err := transport.handleOutput(line); err != nil {
				return err
			}
			if transport.ready && len(waitingForReady) > 0 {
				for _, request := range waitingForReady {
					transport.handleControlRequest(request)
				}
				waitingForReady = nil
			}
		case request, ok := <-control.Requests():
			if !ok {
				continue
			}
			if !transport.ready && !transport.stopping && !transport.shutdownPending {
				if len(waitingForReady) >= 32 {
					request.Respond(fmt.Errorf("RPC runtime startup queue is full"))
				} else {
					waitingForReady = append(waitingForReady, request)
				}
				continue
			}
			transport.handleControlRequest(request)
		}
	}
}

func (t *nativeRPCTransport) handleControlRequest(request agent.ControlSocketRequest) {
	if !t.ready || t.stopping || t.shutdownPending {
		request.Respond(fmt.Errorf("RPC runtime is not accepting commands"))
		return
	}
	err := t.sendCommand(request.Command)
	if err == nil && request.Command.RequireIdle && request.Command.Type == agent.CommandShutdown {
		t.responses[request.Command.ID] = request.Respond
	} else {
		request.Respond(err)
	}
}

func scanRPCOutput(stdout io.Reader, lines chan<- []byte, result chan<- error, log io.Writer) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 50*1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if log != nil {
			_, _ = fmt.Fprintf(log, "%s\n", line)
		}
		lines <- line
	}
	close(lines)
	result <- scanner.Err()
}

func (t *nativeRPCTransport) handleOutput(raw []byte) error {
	var output rpcOutput
	if err := json.Unmarshal(raw, &output); err != nil {
		return t.emit(agent.RuntimeEvent{Type: agent.EventRuntimeFailed, Error: "invalid native Pi RPC output: " + err.Error(), Backend: "pi", BackendEvent: append(json.RawMessage(nil), raw...)})
	}
	if output.Type == "wi_runtime_status" {
		if output.RuntimeID != t.spec.RuntimeID || output.WorkItemID != t.spec.WorkItemID || output.AgentState == "" || !driverprotocol.ValidAgentObservation(output.AgentState, output.LeafID, output.Readiness) {
			return fmt.Errorf("invalid native Pi state observation")
		}
		t.busy = output.AgentState == "active"
		if !t.busy {
			t.lastAssistantFailed = output.AgentState == "failed"
		}
		state, readiness := output.AgentState, output.Readiness
		// Host-submitted work may not have reached Pi yet. A snapshot taken
		// before that handoff cannot make an idle-only command admissible.
		for _, settled := range t.work {
			if !settled {
				state, readiness = "active", "busy"
				break
			}
		}
		// Readers follow the branch tip whatever the run state; a run still
		// held active for submitted work keeps its activity. The reply shown
		// stays until history has it.
		if t.content != nil {
			activity := ""
			switch state {
			case "idle":
				activity = "Idle"
			case "failed":
				activity = "Selected branch ended with failure"
			}
			t.content.SetBranch(output.LeafID, activity)
		}
		return t.emit(agent.RuntimeEvent{Type: agent.EventRuntimeStatus, AgentState: state, LeafID: output.LeafID, Readiness: readiness, Backend: "pi"})
	}
	if output.Type == "response" || output.Type == "wi_control_result" {
		if output.Type == "wi_control_result" && (output.RuntimeID != t.spec.RuntimeID || output.WorkItemID != t.spec.WorkItemID) {
			return fmt.Errorf("native idle-control result owner mismatch")
		}
		if output.ID == rpcReadyCommandID {
			if !output.Success {
				return fmt.Errorf("RPC initialization failed: %s", output.Error)
			}
			var commands struct {
				Commands []struct {
					Name string `json:"name"`
				} `json:"commands"`
			}
			if err := json.Unmarshal(output.Data, &commands); err != nil {
				return fmt.Errorf("read Pi control capabilities: %w", err)
			}
			found := false
			for _, command := range commands.Commands {
				if command.Name == "wi-control-idle" {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("Pi idle-only control extension is unavailable")
			}
			t.ready = true
			return t.emit(agent.RuntimeEvent{Type: agent.EventRuntimeReady, Backend: "pi", BackendEventType: output.Type, BackendEvent: append(json.RawMessage(nil), raw...), Data: decodeJSONObject(output.Data)})
		}
		command, ok := t.pending[output.ID]
		if !ok {
			return t.emit(t.nativeEvent("backend.response", output.Type, raw, ""))
		}
		guardedControl := command.RequireIdle && (command.Type == agent.CommandShutdown || command.Type == agent.CommandAbort)
		if guardedControl && output.Type == "response" && output.Success {
			// Native slash-command handling is not evidence that its guard succeeded.
			return nil
		}
		delete(t.pending, output.ID)
		var disposition struct {
			Disposition string `json:"disposition"`
		}
		_ = json.Unmarshal(output.Data, &disposition)
		if settled, exists := t.work[output.ID]; exists && (!output.Success || disposition.Disposition == "handled" || settled && !t.busy) {
			delete(t.work, output.ID)
		}
		if command.Type == agent.CommandShutdown && command.RequireIdle {
			t.shutdownPending = false
			t.stopping = output.Success
		}
		if respond := t.responses[output.ID]; respond != nil {
			delete(t.responses, output.ID)
			if output.Success {
				respond(nil)
			} else {
				respond(fmt.Errorf("idle-only shutdown refused: %s", output.Error))
			}
		}
		event := agent.RuntimeEvent{
			Type:             agent.EventCommandAccepted,
			CommandID:        command.ID,
			RequestID:        command.RequestID,
			Backend:          "pi",
			BackendEventType: output.Type,
			BackendEvent:     append(json.RawMessage(nil), raw...),
			Data:             map[string]any{"command_type": command.Type, "actor": command.Actor, "native_command": output.Command},
		}
		if !output.Success {
			event.Type = agent.EventCommandRejected
			event.Error = output.Error
			t.forgetPrompt(command.RequestID)
		}
		if err := t.emit(event); err != nil {
			return err
		}
		if command.Type == agent.CommandShutdown && output.Success {
			return t.emit(agent.RuntimeEvent{Type: agent.EventRuntimeStopping, Backend: "pi"})
		}
		return nil
	}

	switch output.Type {
	case "agent_start", "turn_start":
		t.busy = true
		if output.Type == "agent_start" {
			t.lastAssistantFailed = false
		}
	case "agent_settled":
		t.busy = false
		for id := range t.work {
			if _, pending := t.pending[id]; pending {
				t.work[id] = true
			} else {
				delete(t.work, id)
			}
		}
	}
	t.resolveSaved()
	if output.Type == "message_start" {
		if requestID := requestMarkerFromNativeEvent(raw); requestID != "" {
			t.currentRequestID = requestID
		}
	}
	event := t.nativeEvent(normalizedPiEventType(output.Type), output.Type, raw, t.currentRequestID)
	populateNormalizedPiEvent(&event, raw)
	if thought := thinkingDelta(event); thought != "" {
		t.content.Thought(thought)
	} else if updateKind(event) == "thinking_start" {
		// Models that keep their reasoning private still say they are thinking.
		t.content.SetActivity("Thinking")
	}
	if (output.Type == "message_end" || output.Type == "turn_end") && event.Role == "assistant" {
		message, _ := event.Data["message"].(map[string]any)
		reason, _ := message["stopReason"].(string)
		t.lastAssistantFailed = reason == "error" || reason == "aborted"
		if timestamp, ok := message["timestamp"].(float64); ok && output.Type == "message_end" && event.Text != "" {
			t.unsaved = append(t.unsaved, unsavedMessage{role: "assistant", text: event.Text, timestamp: int64(timestamp), since: time.Now()})
		}
	}
	if output.Type == "message_end" && event.Role == "user" {
		t.matchPrompt(event)
	}
	if output.Type == "agent_settled" {
		event.Failed = t.lastAssistantFailed
		event.Outcome = "completed"
		if event.Failed {
			event.Outcome = "failed"
		}
	}
	if output.Type == "agent_settled" {
		defer func() { t.currentRequestID = "" }()
	}
	return t.emit(event)
}

func (t *nativeRPCTransport) nativeEvent(eventType, backendType string, raw []byte, requestID string) agent.RuntimeEvent {
	return agent.RuntimeEvent{
		Type:             eventType,
		RequestID:        requestID,
		Backend:          "pi",
		BackendEventType: backendType,
		BackendEvent:     append(json.RawMessage(nil), raw...),
	}
}

func (t *nativeRPCTransport) emit(event agent.RuntimeEvent) error {
	event.ProtocolVersion = agent.RuntimeProtocolVersion
	event.RuntimeID = t.spec.RuntimeID
	event.WorkItemID = t.spec.WorkItemID
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	t.ensureRuntimeEventID(&event)
	input, output := eventDataText(event.Data, "args"), eventDataText(event.Data, "partial_result")
	if event.Type == agent.EventToolCompleted {
		output = eventDataText(event.Data, "result")
	}
	failed, _ := event.Data["is_error"].(bool)
	// Only assistant messages are live text; Pi also starts and ends messages
	// for prompts and tool results, which must not reset the reply or its
	// parallel tool calls.
	if !(event.Type == agent.EventMessageCompleted || event.Type == "message.started") || event.Role == "assistant" {
		t.content.UpdateTool(event.Type, event.Text, event.ToolCallID, event.ToolName, input, output, failed || event.Error != "")
	}
	t.emitLiveConfirmed(event)
	return nil
}

func eventDataText(data map[string]any, key string) string {
	value, ok := data[key]
	if !ok {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return ""
	}
	return string(encoded)
}

func (t *nativeRPCTransport) emitLiveConfirmed(event agent.RuntimeEvent) {
	ack := make(chan struct{})
	if !t.queueLiveEvent(event, ack) {
		return
	}
	select {
	case <-ack:
	case <-t.liveContext.Done():
	case <-time.After(2500 * time.Millisecond):
	}
}

func (t *nativeRPCTransport) queueLiveEvent(event agent.RuntimeEvent, ack chan struct{}) bool {
	if t.liveDisabled.Load() || !retainedLiveEvent(event.Type) {
		return false
	}
	t.ensureRuntimeEventID(&event)
	semantic := driverprotocol.RuntimeEvent{ProtocolVersion: driverprotocol.RuntimeVersion, EventID: event.EventID, RuntimeID: t.spec.RuntimeID, WorkItemID: t.spec.WorkItemID, Type: event.Type, Timestamp: event.Timestamp, CommandID: event.CommandID, RequestID: event.RequestID, Role: event.Role, ToolName: event.ToolName, Outcome: event.Outcome, AgentState: event.AgentState, LeafID: event.LeafID, Readiness: event.Readiness, ReasonCode: event.ReasonCode, Failed: event.Failed || event.Type == agent.EventRuntimeFailed || event.Type == agent.EventCommandRejected}
	select {
	case t.liveQueue <- queuedLiveEvent{event: semantic, ack: ack}:
		return true
	case <-t.liveContext.Done():
		return false
	}
}

func (t *nativeRPCTransport) runLiveSender(ctx context.Context) {
	for {
		var queued queuedLiveEvent
		select {
		case <-ctx.Done():
			return
		case queued = <-t.liveQueue:
		}
		for {
			requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err := driverprotocol.SubmitEvent(requestCtx, t.spec.EventSocketPath, queued.event)
			cancel()
			if err == nil {
				if queued.ack != nil {
					close(queued.ack)
				}
				break
			}
			var rejected *driverprotocol.EventRejectedError
			if errors.As(err, &rejected) && !rejected.Retryable {
				t.liveDisabled.Store(true)
				if queued.ack != nil {
					close(queued.ack)
				}
				fmt.Fprintf(os.Stderr, "wi rejected runtime event %s: %v\n", queued.event.Type, err)
				return
			}
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

func (t *nativeRPCTransport) ensureRuntimeEventID(event *agent.RuntimeEvent) {
	if event.EventID != "" {
		return
	}
	t.liveSequence++
	event.EventID = fmt.Sprintf("%s:%s:%d", t.spec.RuntimeID, t.liveNonce, t.liveSequence)
}

func retainedLiveEvent(eventType string) bool {
	switch eventType {
	case agent.EventRuntimeReady, agent.EventRuntimeStatus, agent.EventRuntimeStopping, agent.EventRuntimeFailed, agent.EventCommandAccepted, agent.EventCommandRejected,
		agent.EventAgentStarted, agent.EventAgentSettled, agent.EventTurnStarted, agent.EventTurnEnded, agent.EventMessageCompleted, agent.EventToolStarted, agent.EventToolCompleted:
		return true
	default:
		return false
	}
}

func (t *nativeRPCTransport) sendCommand(command agent.ControlCommand) error {
	if command.ProtocolVersion != agent.RuntimeProtocolVersion {
		return fmt.Errorf("unsupported control protocol %d", command.ProtocolVersion)
	}
	if command.RuntimeID != t.spec.RuntimeID {
		return fmt.Errorf("control command targets runtime %s, expected %s", command.RuntimeID, t.spec.RuntimeID)
	}
	if command.WorkItemID != t.spec.WorkItemID {
		return fmt.Errorf("control command targets work item %s, expected %s", command.WorkItemID, t.spec.WorkItemID)
	}
	if strings.TrimSpace(command.ID) == "" {
		return fmt.Errorf("control command id is required")
	}
	if t.handled[command.ID] {
		return nil
	}
	if err := command.ValidateContent(); err != nil {
		return err
	}
	if command.RequireIdle && (t.busy || len(t.work) != 0) {
		return fmt.Errorf("agent became busy; operation refused without interrupting the turn")
	}
	t.handled[command.ID] = true
	if command.Type == agent.CommandShutdown && !command.RequireIdle {
		t.stopping = true
		if err := t.emit(agent.RuntimeEvent{Type: agent.EventCommandAccepted, CommandID: command.ID, RequestID: command.RequestID, Backend: "pi", Data: map[string]any{"command_type": command.Type, "actor": command.Actor}}); err != nil {
			return err
		}
		if err := t.emit(agent.RuntimeEvent{Type: agent.EventRuntimeStopping, Backend: "pi"}); err != nil {
			return err
		}
		return t.stdin.Close()
	}
	native := map[string]any{"id": command.ID, "type": command.Type}
	if len(command.Images) > 0 {
		images := make([]map[string]string, 0, len(command.Images))
		for _, image := range command.Images {
			images = append(images, map[string]string{"type": "image", "data": image.Data, "mimeType": image.MIMEType})
		}
		native["images"] = images
	}
	switch command.Type {
	case agent.CommandPrompt, agent.CommandSteer:
		// Core send defaults to after-turn delivery. Native Pi models that as a
		// prompt carrying streamingBehavior=steer: it starts immediately while
		// idle and joins the steering queue while busy.
		native["type"] = "prompt"
		native["message"] = command.Message
		native["streamingBehavior"] = "steer"
	case agent.CommandFollowUp:
		native["type"] = "follow_up"
		native["message"] = command.Message
	case agent.CommandAbort, agent.CommandShutdown:
		// Guarded shutdown is handled by the in-process extension below.
	default:
		return t.emit(agent.RuntimeEvent{Type: agent.EventCommandRejected, CommandID: command.ID, RequestID: command.RequestID, Error: "unsupported runtime control command " + command.Type, Backend: "pi"})
	}
	if command.RequireIdle && (command.Type == agent.CommandShutdown || command.Type == agent.CommandAbort) {
		raw, err := json.Marshal(command)
		if err != nil {
			delete(t.handled, command.ID)
			return err
		}
		native = map[string]any{"id": command.ID, "type": "prompt", "message": "/wi-control-idle " + base64.StdEncoding.EncodeToString(raw)}
	}
	if command.RequireIdle && (command.Type == agent.CommandPrompt || command.Type == agent.CommandSteer || command.Type == agent.CommandFollowUp) {
		native["type"] = "prompt"
		delete(native, "streamingBehavior")
	}
	// Reserve work before acknowledging handoff. Native events can arrive later.
	isWork := command.Type == agent.CommandPrompt || command.Type == agent.CommandSteer || command.Type == agent.CommandFollowUp
	if isWork {
		if t.work == nil {
			t.work = map[string]bool{}
		}
		t.work[command.ID] = false
	}
	t.pending[command.ID] = command
	if err := t.writeNativeCommand(native); err != nil {
		delete(t.pending, command.ID)
		delete(t.work, command.ID)
		delete(t.handled, command.ID)
		return err
	}
	if isWork && command.RequestID != "" {
		t.prompts = append(t.prompts, submittedPrompt{requestID: command.RequestID, text: strings.TrimSpace(command.Message)})
		if len(t.prompts) > 16 {
			t.prompts = t.prompts[1:]
		}
	}
	if command.Type == agent.CommandShutdown {
		t.shutdownPending = true
	}
	// A successful write transfers ownership to Pi's native RPC transport.
	// Publish compact actionability before replying to the control client; the
	// later backend response remains separate confirmation evidence.
	if command.Type == agent.CommandShutdown {
		return nil
	}
	t.emitLiveConfirmed(agent.RuntimeEvent{Type: agent.EventCommandAccepted, RequestID: command.RequestID, Timestamp: time.Now().UTC()})
	return nil
}

func (t *nativeRPCTransport) writeNativeCommand(command any) error {
	encoded, err := json.Marshal(command)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if _, err := t.stdin.Write(encoded); err != nil {
		return fmt.Errorf("write Pi RPC command: %w", err)
	}
	return nil
}

func normalizedPiEventType(eventType string) string {
	switch eventType {
	case "agent_start":
		return agent.EventAgentStarted
	case "agent_end":
		return "agent.ended"
	case "agent_settled":
		return agent.EventAgentSettled
	case "turn_start":
		return agent.EventTurnStarted
	case "turn_end":
		return agent.EventTurnEnded
	case "message_start":
		return "message.started"
	case "message_update":
		return agent.EventMessageDelta
	case "message_end":
		return agent.EventMessageCompleted
	case "tool_execution_start":
		return agent.EventToolStarted
	case "tool_execution_update":
		return agent.EventToolUpdated
	case "tool_execution_end":
		return agent.EventToolCompleted
	case "queue_update":
		return agent.EventQueueChanged
	case "extension_error":
		return agent.EventRuntimeFailed
	default:
		return "backend." + strings.ReplaceAll(eventType, "_", ".")
	}
}

func populateNormalizedPiEvent(event *agent.RuntimeEvent, raw []byte) {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return
	}
	if message, ok := value["message"].(map[string]any); ok {
		event.Role, _ = message["role"].(string)
		if event.Type == agent.EventMessageCompleted {
			event.Text = textFromNativeContent(message["content"])
			event.Data = map[string]any{"stop_reason": message["stopReason"], "message": message}
		}
	}
	if update, ok := value["assistantMessageEvent"].(map[string]any); ok {
		if updateType, _ := update["type"].(string); updateType == "text_delta" {
			event.Text, _ = update["delta"].(string)
		}
		event.Data = map[string]any{"assistant_message_event": update}
	}
	event.ToolCallID, _ = value["toolCallId"].(string)
	event.ToolName, _ = value["toolName"].(string)
	for source, destination := range map[string]string{
		"args": "args", "partialResult": "partial_result", "result": "result",
		"isError": "is_error", "steering": "steering", "followUp": "follow_up",
		"message": "message", "toolResults": "tool_results",
	} {
		if detail, ok := value[source]; ok {
			if event.Data == nil {
				event.Data = map[string]any{}
			}
			event.Data[destination] = detail
		}
	}
	if errText, _ := value["error"].(string); errText != "" {
		event.Error = errText
	}
}

func requestMarkerFromNativeEvent(raw []byte) string {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	message, _ := value["message"].(map[string]any)
	if role, _ := message["role"].(string); role != "user" {
		return ""
	}
	text := textFromNativeContent(message["content"])
	const prefix = "[wi request "
	start := strings.Index(text, prefix)
	if start < 0 {
		return ""
	}
	remaining := text[start+len(prefix):]
	end := strings.IndexByte(remaining, ']')
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(remaining[:end])
}

func textFromNativeContent(content any) string {
	if text, ok := content.(string); ok {
		return text
	}
	blocks, _ := content.([]any)
	parts := make([]string, 0, len(blocks))
	for _, rawBlock := range blocks {
		block, _ := rawBlock.(map[string]any)
		if blockType, _ := block["type"].(string); blockType == "text" {
			if text, _ := block["text"].(string); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func decodeJSONObject(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return value
}

func isExpectedProcessExit(err error) bool {
	if err == nil {
		return true
	}
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 0
}

type unsavedMessage struct {
	role, text, requestID string
	timestamp             int64
	since                 time.Time
}

// unsavedPatience bounds how long a message's session entry is looked for;
// Pi may create the session file only after the first reply.
const unsavedPatience = time.Minute

// forgetPrompt drops a prompt Pi refused, so a later prompt of the same text
// is not paired with it.
func (t *nativeRPCTransport) forgetPrompt(requestID string) {
	for i, prompt := range t.prompts {
		if requestID != "" && prompt.requestID == requestID {
			t.prompts = append(t.prompts[:i:i], t.prompts[i+1:]...)
			return
		}
	}
}

type submittedPrompt struct {
	requestID, text string
}

// matchPrompt pairs a saved user message with the oldest submitted prompt of
// the same text, so its sender can be told which entry holds it.
func (t *nativeRPCTransport) matchPrompt(event agent.RuntimeEvent) {
	message, _ := event.Data["message"].(map[string]any)
	timestamp, ok := message["timestamp"].(float64)
	if !ok {
		return
	}
	text := strings.TrimSpace(event.Text)
	for i, prompt := range t.prompts {
		if prompt.text == text {
			t.prompts = append(t.prompts[:i:i], t.prompts[i+1:]...)
			t.unsaved = append(t.unsaved, unsavedMessage{role: "user", requestID: prompt.requestID, timestamp: int64(timestamp), since: time.Now()})
			return
		}
	}
}

// resolveSaved finds the session entries of recent messages, which Pi has
// appended by the time it sends its next event, and reports them: an
// assistant message so readers can match its live text with history, a
// prompt so its sender can.
func (t *nativeRPCTransport) resolveSaved() {
	if len(t.unsaved) == 0 {
		return
	}
	if t.content == nil || t.spec.SessionPath == "" {
		t.unsaved = nil
		return
	}
	remaining := t.unsaved[:0]
	for _, pending := range t.unsaved {
		if id, found := sessionEntryID(t.spec.SessionPath, pending.role, pending.timestamp); found {
			if pending.role == "user" {
				t.content.PromptSaved(pending.requestID, []string{id})
			} else {
				t.content.MarkSaved(pending.text, []string{id})
			}
			continue
		}
		if time.Since(pending.since) < unsavedPatience {
			remaining = append(remaining, pending)
		}
	}
	t.unsaved = remaining
}

// updateKind names a message update's native type, such as text_delta.
func updateKind(event agent.RuntimeEvent) string {
	update, _ := event.Data["assistant_message_event"].(map[string]any)
	kind, _ := update["type"].(string)
	return kind
}

// thinkingDelta returns the reasoning a message update adds, if any.
func thinkingDelta(event agent.RuntimeEvent) string {
	if updateKind(event) != "thinking_delta" {
		return ""
	}
	update, _ := event.Data["assistant_message_event"].(map[string]any)
	delta, _ := update["delta"].(string)
	return delta
}
