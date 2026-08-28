package pi

import (
	"bufio"
	"context"
	"crypto/rand"
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

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/coordinator"
)

const rpcReadyCommandID = "wi-runtime-ready"

type rpcOutput struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Command string          `json:"command,omitempty"`
	Success bool            `json:"success,omitempty"`
	Error   string          `json:"error,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type queuedLiveEvent struct {
	event coordinator.RuntimeSemanticEvent
	ack   chan struct{}
}

type nativeRPCTransport struct {
	spec             ExecSpec
	stdin            io.WriteCloser
	pending          map[string]agent.ControlCommand
	handled          map[string]bool
	ready            bool
	stopping         bool
	currentRequestID string
	liveClient       *coordinator.Client
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
	if strings.TrimSpace(spec.DaemonSocketPath) == "" {
		return fmt.Errorf("native Pi RPC requires a daemon socket path")
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
		liveNonce: hex.EncodeToString(nonceBytes),
	}
	transport.liveClient = &coordinator.Client{SocketPath: spec.DaemonSocketPath}
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
	if err := transport.writeNativeCommand(map[string]any{"id": rpcReadyCommandID, "type": "get_state"}); err != nil {
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

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-processDone:
			if transport.stopping && isExpectedProcessExit(err) {
				return nil
			}
			return err
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
		case request, ok := <-control.Requests():
			if !ok {
				continue
			}
			if !transport.ready || transport.stopping {
				request.Respond(fmt.Errorf("RPC runtime is not accepting commands"))
				continue
			}
			request.Respond(transport.sendCommand(request.Command))
		}
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
	if output.Type == "response" {
		if output.ID == rpcReadyCommandID {
			if !output.Success {
				return fmt.Errorf("RPC initialization failed: %s", output.Error)
			}
			t.ready = true
			return t.emit(agent.RuntimeEvent{Type: agent.EventRuntimeReady, Backend: "pi", BackendEventType: output.Type, BackendEvent: append(json.RawMessage(nil), raw...), Data: decodeJSONObject(output.Data)})
		}
		command, ok := t.pending[output.ID]
		if !ok {
			return t.emit(t.nativeEvent("backend.response", output.Type, raw, ""))
		}
		delete(t.pending, output.ID)
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
		}
		return t.emit(event)
	}

	if output.Type == "message_start" {
		if requestID := requestMarkerFromNativeEvent(raw); requestID != "" {
			t.currentRequestID = requestID
		}
	}
	event := t.nativeEvent(normalizedPiEventType(output.Type), output.Type, raw, t.currentRequestID)
	populateNormalizedPiEvent(&event, raw)
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
	t.emitLiveConfirmed(event)
	return nil
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
	if t.liveClient == nil || t.liveDisabled.Load() || !retainedLiveEvent(event.Type) {
		return false
	}
	t.ensureRuntimeEventID(&event)
	semantic := coordinator.RuntimeSemanticEvent{ID: event.EventID, RuntimeID: t.spec.RuntimeID, WorkItemID: t.spec.WorkItemID, Type: event.Type, Timestamp: event.Timestamp, RequestID: event.RequestID, Role: event.Role, ToolName: event.ToolName, Failed: event.Type == agent.EventRuntimeFailed || event.Type == agent.EventCommandRejected}
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
			_, err := t.liveClient.IngestRuntimeEvent(requestCtx, queued.event)
			cancel()
			if err == nil {
				if queued.ack != nil {
					close(queued.ack)
				}
				break
			}
			if permanentRuntimeIngestError(err) {
				t.liveDisabled.Store(true)
				if queued.ack != nil {
					close(queued.ack)
				}
				fmt.Fprintf(os.Stderr, "wi daemon rejected runtime event %s: %v; restart the older process\n", queued.event.Type, err)
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

func permanentRuntimeIngestError(err error) bool {
	var protocolErr *coordinator.ProtocolError
	if errors.As(err, &protocolErr) {
		return protocolErr.Code == "build_mismatch" || protocolErr.Code == "protocol_mismatch" || protocolErr.Code == "runtime_owner_mismatch"
	}
	var compatibilityErr *coordinator.CompatibilityError
	if errors.As(err, &compatibilityErr) {
		return compatibilityErr.Kind == "build" || compatibilityErr.Kind == "protocol"
	}
	return false
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
	case agent.EventRuntimeReady, agent.EventRuntimeStopping, agent.EventRuntimeFailed, agent.EventCommandAccepted, agent.EventCommandRejected,
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
	t.handled[command.ID] = true
	if command.Type == agent.CommandShutdown {
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
	case agent.CommandAbort:
		// No additional fields.
	default:
		return t.emit(agent.RuntimeEvent{Type: agent.EventCommandRejected, CommandID: command.ID, RequestID: command.RequestID, Error: "unsupported runtime control command " + command.Type, Backend: "pi"})
	}
	t.pending[command.ID] = command
	if err := t.writeNativeCommand(native); err != nil {
		delete(t.pending, command.ID)
		return err
	}
	// A successful write transfers ownership to Pi's native RPC transport.
	// Publish compact actionability before replying to the control client; the
	// later backend response remains separate confirmation evidence.
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
