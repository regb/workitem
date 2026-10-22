package claude

import (
	"context"
	"errors"
	"fmt"
	protocol "github.com/regb/workitem/agentdriver/protocol"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// headlessDisallowedTools removes tools that wait for an interactive answer.
// A headless runtime has no channel for replying to an agent's question, so
// the agent proceeds on its own judgment instead of stalling the turn.
const headlessDisallowedTools = "AskUserQuestion"

func launchHeadless(ctx context.Context, r protocol.LaunchRequest, c Config, reply func(protocol.Ready) error) error {
	replied := false
	fail := func(err error) error {
		if !replied {
			replied = true
			_ = reply(protocol.Ready{ProtocolVersion: protocol.Version, Error: err.Error()})
		}
		return err
	}
	prepared, err := prepareRuntime(r, c)
	if err != nil {
		return fail(err)
	}
	defer prepared.close()
	executable, session, env := prepared.executable, prepared.session, prepared.env
	control, contentServer := prepared.control, prepared.content
	args, err := prepared.args("--print", "--verbose", "--input-format", "stream-json", "--output-format", "stream-json", "--include-partial-messages", "--replay-user-messages", "--disallowed-tools", headlessDisallowedTools, "--permission-prompts", "none")
	if err != nil {
		return fail(err)
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = r.Runtime.CWD
	cmd.Env = env
	cmd.Stderr = os.Stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		return fail(err)
	}
	defer input.Close()
	writer, ok := input.(*os.File)
	if !ok {
		return fail(fmt.Errorf("Claude stdin is not a deadline-capable pipe"))
	}
	// Own the stdout pipe so Wait cannot close it before the final frames drain.
	output, outWriter, err := os.Pipe()
	if err != nil {
		return fail(err)
	}
	defer output.Close()
	cmd.Stdout = outWriter
	if err := cmd.Start(); err != nil {
		outWriter.Close()
		return fail(err)
	}
	outWriter.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	processDone := false
	var processErr error
	var drainTimer *time.Timer
	var drainDeadline <-chan time.Time
	defer func() {
		if drainTimer != nil {
			drainTimer.Stop()
		}
	}()
	defer func() {
		input.Close()
		if !processDone {
			_ = cmd.Process.Kill()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
			}
		}
	}()
	readCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()
	frames := make(chan headlessRead, 1)
	go readHeadlessFrames(readCtx, output, frames)
	sequence := uint64(0)
	eventParent := ctx
	emit := func(event protocol.RuntimeEvent) {
		sequence++
		if event.Type == protocol.EventAgentSettled {
			event.Outcome = "completed"
			if event.Failed {
				event.Outcome = "failed"
			}
		}
		event.ProtocolVersion = protocol.RuntimeVersion
		event.EventID = fmt.Sprintf("claude:%s:%d", r.Runtime.ID, sequence)
		event.RuntimeID = r.Runtime.ID
		event.WorkItemID = r.Runtime.WorkItemID
		event.Timestamp = time.Now().UTC()
		eventCtx, cancel := context.WithTimeout(eventParent, 500*time.Millisecond)
		defer cancel()
		_ = protocol.SubmitEvent(eventCtx, r.Runtime.EventSocketPath, event)
	}
	state := &headlessControls{input: writer, emit: emit, session: session}
	defer func() {
		if !processDone {
			_ = cmd.Process.Kill()
		}
		// Report pending outcomes without letting an unavailable event endpoint
		// delay process teardown once per queued command.
		closing, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		eventParent = closing
		state.close()
	}()
	display := &headlessContent{server: contentServer, emit: emit}
	// confirm records Claude's receipt of a prompt and links it, for its
	// sender, to the transcript record: wi chose that record's uuid.
	confirm := func(id string) {
		state.confirm(id)
		if d := state.work[id]; d != nil {
			contentServer.PromptSaved(d.command.RequestID, []string{id})
		}
	}
	emit(protocol.RuntimeEvent{Type: protocol.EventRuntimeStatus, Readiness: "starting", ReasonCode: "headless_initializing"})
	initID := "wi-headless-initialize"
	if _, err := writeHeadlessFrame(writer, headlessControl(initID, "initialize")); err != nil {
		return fail(err)
	}
	initialization := time.NewTimer(15 * time.Second)
	defer initialization.Stop()
	initDeadline := initialization.C
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var stopTimer *time.Timer
	defer func() {
		if stopTimer != nil {
			stopTimer.Stop()
		}
	}()
	lastReadiness := ""
	status := func() {
		readiness, reason := "ready", "headless_ready"
		if !state.initialized {
			readiness, reason = "starting", "headless_initializing"
		} else if state.stopping {
			readiness, reason = "stopping", "runtime_stopping"
		} else if state.inputBroken {
			readiness, reason = "blocked", "headless_input_unavailable"
		} else if state.nativeBusy {
			readiness, reason = "busy", "active_turn"
		} else if state.busy() {
			readiness, reason = "awaiting_confirmation", "execution_pending"
		}
		key := readiness + ":" + reason
		if key != lastReadiness {
			lastReadiness = key
			emit(protocol.RuntimeEvent{Type: protocol.EventRuntimeStatus, Readiness: readiness, ReasonCode: reason})
		}
	}
	started := false
	startWork := func() {
		state.nativeBusy = true
		if !started {
			started = true
			display.text = ""
			display.truncated = false
			display.activity = "Working"
			display.publish()
			emit(protocol.RuntimeEvent{Type: protocol.EventAgentStarted})
		}
	}
	// contentFault notes a frame whose content could not be shown. Live content
	// is display only: the frame is skipped and the agent keeps working.
	contentFault := func(frameType string, err error) {
		fmt.Fprintf(os.Stderr, "Claude %s frame content skipped: %v\n", frameType, err)
	}
	// protocolFailure ends the runtime, naming the cause in its log without
	// native content.
	protocolFailure := func(cause string) error {
		emit(protocol.RuntimeEvent{Type: protocol.EventRuntimeFailed, Failed: true, ReasonCode: "headless_protocol_error"})
		return fail(fmt.Errorf("Claude headless protocol failed: %s", cause))
	}
	for {
		select {
		case <-initDeadline:
			return fail(fmt.Errorf("Claude headless initialization timed out"))
		case <-ctx.Done():
			return fail(ctx.Err())
		case err := <-done:
			processDone = true
			processErr = err
			done = nil
			if state.stopping {
				return nil
			}
			drainTimer = time.NewTimer(3 * time.Second)
			drainDeadline = drainTimer.C
		case <-drainDeadline:
			return protocolFailure(fmt.Sprintf("Claude exited (%v) and its output did not end", processErr))
		case <-ticker.C:
			state.tick(time.Now())
			status()
		case item := <-frames:
			if item.err != nil {
				if !errors.Is(item.err, io.EOF) {
					return protocolFailure(item.err.Error())
				}
				if processDone {
					if state.stopping {
						return nil
					}
					if processErr != nil || state.busy() {
						return protocolFailure(fmt.Sprintf("Claude exited (%v) while busy=%v", processErr, state.busy()))
					}
					emit(protocol.RuntimeEvent{Type: protocol.EventRuntimeFailed, Failed: true, ReasonCode: "runtime_stopped"})
					return fail(fmt.Errorf("Claude headless exited unexpectedly"))
				}
				select {
				case err := <-done:
					processDone = true
					if state.stopping {
						return nil
					}
					if err != nil || state.busy() {
						return protocolFailure(fmt.Sprintf("Claude exited (%v) while busy=%v", err, state.busy()))
					}
					emit(protocol.RuntimeEvent{Type: protocol.EventRuntimeFailed, Failed: true, ReasonCode: "runtime_stopped"})
					return fail(fmt.Errorf("Claude headless exited unexpectedly"))
				case <-time.After(3 * time.Second):
					return protocolFailure("Claude's output ended but the process did not exit")
				}
			}
			f := item.frame
			if f.SessionID != "" && f.SessionID != session {
				return protocolFailure("a frame belongs to another session")
			}
			if f.ParentToolID != nil && *f.ParentToolID != "" {
				continue
			}
			if len(f.UserIDs) > 1024 || len(f.Response.Response.Cancelled) > 1024 || len(f.Response.Response.Queued) > 1024 {
				return protocolFailure("a " + f.Type + " frame exceeds its identity bounds")
			}
			if state.retiredFrame(f) || f.IsReplay && f.Type != "user" {
				continue
			}
			switch f.Type {
			case "control_response":
				if f.Response.RequestID == initID && !state.initialized {
					if f.Response.Subtype != "success" {
						return fail(fmt.Errorf("Claude rejected headless initialization"))
					}
					if !supportedHeadlessVersion(f.Response.Response.Version) {
						return fail(fmt.Errorf("Claude headless requires Claude Code 2.1.299 or later"))
					}
					if f.Response.Response.State == "" {
						return protocolFailure("initialization reported no session state")
					}
					state.initialized = true
					state.nativeBusy = f.Response.Response.State != "idle"
					initDeadline = nil
					initialization.Stop()
					replied = true
					if err := reply(protocol.Ready{ProtocolVersion: protocol.Version, BackendPath: executable, BackendVersion: f.Response.Response.Version, Capabilities: protocol.Capabilities{Headless: true, LiveContent: true, Streaming: true, ImageInput: true, BundledSkills: true, Monitor: true, Prompt: true, Steering: true, FollowUp: true, Cancel: true, Control: protocol.ControlStructured, Settings: []string{"model", "effort"}}}); err != nil {
						return err
					}
					emit(protocol.RuntimeEvent{Type: protocol.EventRuntimeReady, Readiness: "ready", ReasonCode: "headless_ready"})
				} else {
					state.interruptResponse(f)
					state.settingResponse(f)
				}
			case "control_request":
				response := map[string]any{"subtype": "error", "request_id": f.RequestID, "error": "Unsupported headless control request"}
				if f.Request.Subtype == "can_use_tool" {
					response = map[string]any{"subtype": "success", "request_id": f.RequestID, "response": map[string]any{"behavior": "deny", "message": "Interactive approval or user input is unavailable in Wi headless mode. Ask in the conversation instead."}}
					emit(protocol.RuntimeEvent{Type: protocol.EventRuntimeStatus, Readiness: "blocked", ReasonCode: "interaction_required"})
				}
				if _, err := writeHeadlessFrame(writer, map[string]any{"type": "control_response", "response": response}); err != nil {
					return protocolFailure("could not answer a control request: " + err.Error())
				}
			case "system":
				if f.Subtype == "init" {
					if !supportedHeadlessCapabilities(f.Capabilities) {
						return protocolFailure("Claude lacks a required headless capability")
					}
					state.cancelQueued = true
				}
				if f.Subtype == "status" && f.Status == "requesting" {
					if !state.cancelQueued {
						return protocolFailure("Claude requested work before initialization")
					}
					startWork()
				}
			case "command_lifecycle":
				if f.State == "queued" {
					confirm(f.CommandUUID)
				}
				if f.State == "started" {
					confirm(f.CommandUUID)
					if d := state.work[f.CommandUUID]; d != nil {
						d.started = true
					}
				}
				if f.State == "rejected" || f.State == "cancelled" {
					if d := state.work[f.CommandUUID]; d != nil {
						delivery, reason := "confirmed", "native_command_rejected"
						if f.State == "cancelled" {
							delivery, reason = "cancelled", "native_queued_cancelled"
							d.phase = "cancelled"
						} else {
							d.phase = "failed"
						}
						// Executing work still needs its result; a queue cancellation is
						// not a turn-settlement receipt.
						if !d.started {
							delete(state.work, f.CommandUUID)
						}
						d.err = fmt.Errorf("Claude rejected or cancelled submitted input")
						state.fact(protocol.EventCommandRejected, d, true, delivery, reason)
					}
				}
			case "user":
				if f.IsReplay {
					confirm(f.UUID)
				} else if err := display.frame(f); err != nil {
					contentFault(f.Type, err)
				}
			case "stream_event", "assistant":
				if !state.cancelQueued && (f.Type != "assistant" || f.Error == "") {
					return protocolFailure("Claude streamed a reply before initialization")
				}
				for _, id := range f.UserIDs {
					state.confirm(id)
				}
				if f.UserID != "" {
					state.confirm(f.UserID)
				}
				// A provider error is not evidence that a model produced an answer.
				// Its private native text can still be displayed; the result settles
				// the failed turn without retaining that text in compact events.
				if f.Type != "assistant" || f.Error == "" {
					startWork()
				}
				if err := display.frame(f); err != nil {
					contentFault(f.Type, err)
				}
			case "result":
				matched, failed := state.result(f)
				if matched || len(state.work) == 0 {
					started = false
					display.activity = "Idle"
					if failed {
						display.activity = "Turn failed"
					}
					display.publish()
				}
			}
			status()
		case request := <-control.Requests():
			c := request.Command
			if c.ProtocolVersion != protocol.RuntimeVersion || c.RuntimeID != r.Runtime.ID || c.WorkItemID != r.Runtime.WorkItemID {
				request.Respond(fmt.Errorf("runtime identity mismatch"))
				continue
			}
			if c.Type != protocol.CommandShutdown {
				request.Respond(state.accept(c))
				status()
				continue
			}
			if c.RequireIdle {
				request.Respond(fmt.Errorf("Claude does not support atomic idle-only shutdown"))
				continue
			}
			if state.stopping {
				request.Respond(nil)
				continue
			}
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				request.Respond(err)
				continue
			}
			state.stopping = true
			// Escalation must not wait behind compact-event delivery or queue cleanup.
			stopTimer = time.AfterFunc(3*time.Second, func() { _ = cmd.Process.Kill() })
			request.Respond(nil)
			// Queue cancellation shares one reporting deadline. A reachable but
			// unresponsive event endpoint must not add a timeout per queued item.
			closing, cancel := context.WithTimeout(ctx, time.Second)
			eventParent = closing
			state.cancelOwnQueue()
			emit(protocol.RuntimeEvent{Type: protocol.EventRuntimeStopping})
			status()
			cancel()
			eventParent = ctx

		}
	}
}
