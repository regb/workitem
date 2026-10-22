// Package claude implements Claude Code TUI and headless Wi runtimes. Claude
// owns the transcript format; Wi owns the conversation and runtime identities.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agentdriver/discovery"
	"github.com/regb/workitem/internal/version"
)

type Config struct {
	Executable string `json:"executable,omitempty"`
	Model      string `json:"model,omitempty"`
	Effort     string `json:"effort,omitempty"`
}

func parseConfig(raw json.RawMessage) (Config, error) {
	var c Config
	if len(raw) > 0 {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err := d.Decode(&c); err != nil {
			return c, err
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			return c, fmt.Errorf("one configuration object required")
		}
	}
	c.Executable = strings.TrimSpace(c.Executable)
	if c.Executable == "" {
		c.Executable = "claude"
	}
	if strings.HasPrefix(c.Model, "-") || strings.ContainsAny(c.Model, "\r\n\x00") {
		return c, fmt.Errorf("invalid Claude model")
	}
	switch c.Effort {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		return c, fmt.Errorf("invalid Claude effort %q", c.Effort)
	}
	return c, nil
}
func Run(ctx context.Context) error {
	request, reply, err := protocol.ConnectRequest(ctx, os.Getenv(protocol.BootstrapSocketEnv), protocol.Hello{ProtocolVersion: protocol.Version, Driver: "claude", DriverVersion: version.BuildIdentity()})
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = reply(protocol.Response{ProtocolVersion: protocol.Version, Error: err.Error()})
		return err
	}
	if request.ProtocolVersion != protocol.Version || request.Driver != "claude" {
		return fail(fmt.Errorf("incompatible Claude driver request"))
	}
	if request.Operation == protocol.OperationConfigDiscover {
		result, err := discovery.Executable("claude", "Claude Code", "claude")
		if err != nil {
			return fail(err)
		}
		return reply(protocol.Response{ProtocolVersion: protocol.Version, ConfigDiscovery: &result})
	}
	config, err := parseConfig(request.Config)
	if err != nil {
		return fail(err)
	}
	if request.Operation == protocol.OperationConfigValidate {
		return reply(protocol.Response{ProtocolVersion: protocol.Version, ConfigValidation: &protocol.ConfigValidationResult{Valid: true}})
	}
	if request.Operation == protocol.OperationConfigModels {
		catalog, err := ModelCatalog(ctx, config)
		if err != nil {
			return fail(err)
		}
		return reply(protocol.Response{ProtocolVersion: protocol.Version, ModelCatalog: &catalog})
	}
	if request.Operation == protocol.OperationRuntimeModes {
		modes := RuntimeModes()
		return reply(protocol.Response{ProtocolVersion: protocol.Version, RuntimeModes: &modes})
	}
	if request.Operation != protocol.OperationLaunch {
		result, err := conversationOperationContext(ctx, request)
		if err != nil {
			return fail(err)
		}
		return reply(protocol.Response{ProtocolVersion: protocol.Version, Conversation: &result})
	}
	if request.Launch == nil {
		return fail(fmt.Errorf("Claude launch request is required"))
	}
	config, err = parseConfig(request.Launch.Config)
	if err != nil {
		return fail(err)
	}
	return launch(ctx, *request.Launch, config, func(ready protocol.Ready) error {
		return reply(protocol.Response{ProtocolVersion: protocol.Version, Ready: &ready, Error: ready.Error})
	})
}

func launch(ctx context.Context, r protocol.LaunchRequest, c Config, reply func(protocol.Ready) error) error {
	fail := func(err error) error {
		_ = reply(protocol.Ready{ProtocolVersion: protocol.Version, Error: err.Error()})
		return err
	}
	if r.Runtime.Mode == protocol.ModeHeadless {
		return launchHeadless(ctx, r, c, reply)
	}
	if r.Runtime.Mode != protocol.ModeTUI {
		return fail(fmt.Errorf("unsupported Claude runtime mode"))
	}
	prepared, err := prepareRuntime(r, c)
	if err != nil {
		return fail(err)
	}
	defer prepared.close()
	executable, session, native, env := prepared.executable, prepared.session, prepared.native, prepared.env
	control, contentServer := prepared.control, prepared.content
	hookPath := hookSocketPath(r.Runtime.ControlSocketPath)
	hooks, err := protocol.ListenControl(hookPath)
	if err != nil {
		return fail(err)
	}
	defer hooks.Close()
	self, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	settings, cleanup, err := hookSettings(self, hookPath)
	if err != nil {
		return fail(err)
	}
	defer cleanup()
	display, err := protocol.ListenControl(displaySocketPath(hookPath))
	if err != nil {
		return fail(err)
	}
	defer display.Close()
	args, err := prepared.args("--settings", settings)
	if err != nil {
		return fail(err)
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = r.Runtime.CWD
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	input := discoverTUIInput(env)
	if err := reply(protocol.Ready{ProtocolVersion: protocol.Version, BackendPath: executable, Capabilities: protocol.Capabilities{NativeTUI: true, Monitor: true, BundledSkills: true, LiveContent: true, Streaming: true, ImageInput: supportsImageInput(input), Prompt: supportsControl(input, protocol.CommandPrompt), Steering: supportsControl(input, protocol.CommandSteer), FollowUp: supportsControl(input, protocol.CommandFollowUp), Cancel: supportsControl(input, protocol.CommandAbort), Control: protocol.ControlEmulated}}); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var sequence atomic.Uint64
	var closingDeadline time.Time
	emitFact := func(event protocol.RuntimeEvent) error {
		deadline := time.Now().Add(3 * time.Second)
		if !closingDeadline.IsZero() && closingDeadline.Before(deadline) {
			deadline = closingDeadline
		}
		eventCtx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		if event.Type == protocol.EventAgentSettled {
			event.Outcome = "completed"
			if event.Failed {
				event.Outcome = "failed"
			}
		}
		event.ProtocolVersion = protocol.RuntimeVersion
		event.EventID = fmt.Sprintf("claude:%s:%d", r.Runtime.ID, sequence.Add(1))
		event.RuntimeID = r.Runtime.ID
		event.WorkItemID = r.Runtime.WorkItemID
		event.Timestamp = time.Now().UTC()
		return protocol.SubmitEvent(eventCtx, r.Runtime.EventSocketPath, event)
	}
	emit := func(kind string, failed bool, tool string) error {
		return emitFact(protocol.RuntimeEvent{Type: kind, Failed: failed, ToolName: tool})
	}
	var controls *tuiControls
	controls = &tuiControls{input: input, emit: func(kind string, c protocol.ControlCommand, failed bool) {
		state, reason := controls.deliveryFacts(c)
		_ = emitFact(protocol.RuntimeEvent{Type: kind, Failed: failed, CommandID: c.ID, RequestID: c.RequestID, DeliveryState: state, ReasonCode: reason})
	}}
	readiness := &readinessTracker{started: time.Now()}
	readiness.tick(ctx, controls, time.Now(), emitFact)
	defer func() { closingDeadline = time.Now().Add(3 * time.Second); controls.close() }()
	stopping := false
	readySeen := false
	turnFailed := false
	// turnReason says why the turn failed when the transcript says, such as
	// a usage limit.
	turnReason := ""
	turns := &turnTracker{}
	stream := &displayStream{server: contentServer, turns: turns}
	feed := &transcriptFeed{factAfter: readiness.started}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var stopTimer *time.Timer
	var stopDeadline <-chan time.Time
	defer func() {
		if stopTimer != nil {
			stopTimer.Stop()
		}
	}()
	for {
		select {
		case err := <-done:
			_, _ = feed.refresh(r.Conversation.Locator, stream)
			if stopping {
				return nil
			}
			if controls.busy {
				_ = emit(protocol.EventAgentSettled, true, "")
			}
			if err != nil {
				_ = emit(protocol.EventRuntimeFailed, true, "")
			}
			return err
		case <-ticker.C:
			facts, err := feed.refresh(r.Conversation.Locator, stream)
			if err != nil {
				contentServer.PublishTranscript(feed.leaf, feed.text, "Transcript inspection failed", feed.tool)
			}
			for _, fact := range facts {
				if !fact.Timestamp.IsZero() && fact.Timestamp.Before(turns.started.Truncate(time.Millisecond)) {
					continue
				}
				turns.settled = true
				turns.executing = false
				controls.settled()
				turnFailed = true
				reason := settleReason(fact)
				if reason == "prompt_blocked" {
					stream.reset()
				}
				_ = emitFact(protocol.RuntimeEvent{Type: protocol.EventAgentSettled, Failed: true, ReasonCode: reason, CommandID: turns.commandID, RequestID: turns.requestID})
			}
			controls.tick(ctx, time.Now())
			readiness.tick(ctx, controls, time.Now(), emitFact)
		case <-stopDeadline:
			if err := cmd.Process.Kill(); err != nil {
				return fmt.Errorf("terminate Claude after shutdown timeout: %w", err)
			}
			stopDeadline = nil
		case request := <-control.Requests():
			command := request.Command
			if command.ProtocolVersion != protocol.RuntimeVersion || command.RuntimeID != r.Runtime.ID || command.WorkItemID != r.Runtime.WorkItemID {
				request.Respond(fmt.Errorf("runtime identity mismatch"))
				continue
			}
			if command.Type == protocol.CommandPrompt || command.Type == protocol.CommandSteer || command.Type == protocol.CommandFollowUp || command.Type == protocol.CommandAbort {
				err := controls.accept(ctx, command)
				request.Respond(err)
				continue
			}
			if command.Type != protocol.CommandShutdown {
				request.Respond(fmt.Errorf("Claude TUI does not support programmatic %s; use its native interface", command.Type))
				continue
			}
			if command.RequireIdle && (!readySeen || controls.busy || controls.pending != nil || controls.aborting != nil || len(controls.queue) > 0) {
				request.Respond(fmt.Errorf("Claude is busy; shutdown requires idle state"))
				continue
			}
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				request.Respond(err)
				continue
			}
			stopping = true
			controls.stopping = true
			if stopTimer == nil {
				stopTimer = time.NewTimer(3 * time.Second)
				stopDeadline = stopTimer.C
			}
			request.Respond(nil)
			_ = emit(protocol.EventRuntimeStopping, false, "")
		case request := <-display.Requests():
			var h displayInput
			err := json.Unmarshal([]byte(request.Command.Message), &h)
			if request.Command.ProtocolVersion != protocol.RuntimeVersion || request.Command.Type != "display" || h.Event != "MessageDisplay" || h.SessionID != session || filepath.Clean(h.Transcript) != native {
				err = fmt.Errorf("invalid private display identity")
			}
			if err == nil && outOfTurnDisplay(turns, stream, h) {
				request.Respond(nil)
				continue
			}
			if err == nil {
				var accepted bool
				accepted, err = stream.accept(h)
				if accepted {
					controls.busy = true
					controls.executionPending = false
					turns.settled = false
					readiness.nextProbe = time.Time{}
					readiness.tick(ctx, controls, time.Now(), emitFact)
					if !turns.executing {
						turns.executing = true
						_ = emit(protocol.EventAgentStarted, false, "")
					}
				}
			}
			request.Respond(err)
		case request := <-hooks.Requests():
			var h hookInput
			err := json.Unmarshal([]byte(request.Command.Message), &h)
			if request.Command.ProtocolVersion != protocol.RuntimeVersion || request.Command.Type != "hook" {
				err = fmt.Errorf("invalid Claude hook envelope")
			}
			if err == nil && h.SessionID != session {
				err = fmt.Errorf("Claude session identity mismatch")
			}
			if err == nil && h.Event == "SessionStart" && filepath.Clean(h.Transcript) != native {
				err = fmt.Errorf("Claude did not use the Wi-owned transcript namespace")
			}
			if err == nil && len(h.PromptID) > 128 {
				err = fmt.Errorf("invalid native prompt identity")
			}
			if err == nil && !turns.accept(h) {
				request.Respond(nil)
				continue
			}
			if err == nil {
				startExecution := false
				pending := controls.pending
				controls.hook(h.Event, h.PromptDigest)
				asking := controls.asking
				controls.observeQuestion(h.Event, h.Tool, h.Notification)
				if h.Event == "UserPromptSubmit" && pending != nil && pending.phase == "confirmed" {
					turns.commandID = pending.command.ID
					turns.requestID = pending.command.RequestID
				}
				switch h.Event {
				case "UserPromptSubmit":
					turnFailed = false
					turnReason = ""
					stream.reset()
					contentServer.Update("message.started", "", "")
				case "PreToolUse":
					turns.settled = false
					if !turns.executing {
						turns.executing = true
						startExecution = true
					}
				case "SessionStart":
					if !readySeen {
						turns.settled = false
						turns.executing = false
						stream.reset()
					}
					readySeen = true
				case "Stop", "StopFailure":
					if stream.active && !stream.final {
						stream.final = true
						stream.incomplete = true
						stream.publish()
					}
					facts, _ := feed.refresh(r.Conversation.Locator, stream)
					for _, fact := range facts {
						if fact.Timestamp.IsZero() || !fact.Timestamp.Before(turns.started.Truncate(time.Millisecond)) {
							turnFailed = true
							if reason := settleReason(fact); reason != "" {
								turnReason = reason
							}
						}
					}
					if h.Event == "StopFailure" {
						turnFailed = true
						feed.activity = "Turn failed"
						contentServer.PublishTranscript(feed.leaf, feed.text, feed.activity, feed.tool)
					}
					turns.settled = true
					turns.executing = false
					// refresh may already have consumed the final record before the
					// Stop hook. Reconcile again with the settled-turn fence in place.
					stream.PublishTranscript(feed.leaf, feed.text, feed.activity, feed.tool)
				}
				if (h.Event == "UserPromptSubmit" || h.Event == "PreToolUse" || h.Event == "SessionEnd") && controls.asking == asking {
					readiness.nextProbe = time.Time{}
					readiness.tick(ctx, controls, time.Now(), emitFact)
				}
				if startExecution {
					_ = emit(protocol.EventAgentStarted, false, "")
				}
				kind, failed := hookEvent(h.Event)
				if kind == protocol.EventAgentSettled {
					failed = failed || turnFailed
				}
				if kind != "" {
					if len(h.Tool) > 128 {
						err = fmt.Errorf("invalid tool name")
					} else {
						err = emitFact(protocol.RuntimeEvent{Type: kind, Failed: failed, ToolName: h.Tool, ReasonCode: settledReason(kind, failed, turnReason)})
					}
				}
				if controls.asking != asking {
					// After the tool fact, so the question outlasts the work it pauses.
					readiness.nextProbe = time.Time{}
					readiness.tick(ctx, controls, time.Now(), emitFact)
				}
			}
			request.Respond(err)
			if err != nil && h.Event == "SessionStart" {
				_ = cmd.Process.Kill()
				<-done
				return err
			}
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			<-done
			return ctx.Err()
		}
	}
}

// settleReason is the reason code a failed transcript fact gives its turn.
func settleReason(fact protocol.ConversationFact) string {
	switch fact.StopReason {
	case "prompt_blocked", "usage_limit":
		return fact.StopReason
	}
	return ""
}

// settledReason attaches a failed turn's reason to its settlement only.
func settledReason(kind string, failed bool, reason string) string {
	if kind != protocol.EventAgentSettled || !failed {
		return ""
	}
	return reason
}
