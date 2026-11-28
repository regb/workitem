package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agentdriver/acp"
)

const (
	// steeringVersion is the first agy release that steers a running turn
	// with the next prompt; older ones queue it.
	steeringVersion = "1.5.0"
	// quietSettle is how long a turn whose prompt returned, and whose
	// background work ended, stays open for a late reply before it settles.
	quietSettle = 3 * time.Second
	// openReplySettle bounds the wait for a reply agy started but did not
	// end with a usage update.
	openReplySettle = 2 * time.Minute
	// questionPrefix marks the tool call of a fixed-choice question agy asks
	// through a permission request.
	questionPrefix = "interaction_"
	// maxQueue bounds queued follow-ups.
	maxQueue = 32
)

// launch runs Antigravity headless for one runtime until it stops.
func launch(ctx context.Context, r protocol.LaunchRequest, c Config, reply func(protocol.Ready) error) error {
	replied := false
	fail := func(err error) error {
		if !replied {
			replied = true
			_ = reply(protocol.Ready{ProtocolVersion: protocol.Version, Error: err.Error()})
		}
		return err
	}
	if r.Runtime.Mode != protocol.ModeHeadless {
		return fail(fmt.Errorf("Antigravity runs only headless"))
	}
	if !filepath.IsAbs(r.Runtime.CWD) || !filepath.IsAbs(r.Conversation.Locator) {
		return fail(fmt.Errorf("Antigravity requires absolute workspace and conversation paths"))
	}
	executable, err := exec.LookPath(c.Executable)
	if err != nil {
		return fail(err)
	}
	rt := &runtime{request: r, config: c, seen: map[string]error{}}
	if rt.journal, err = openJournal(r.Conversation.Locator); err != nil {
		return fail(err)
	}
	defer rt.journal.close()
	if rt.control, err = protocol.ListenControl(r.Runtime.ControlSocketPath); err != nil {
		return fail(err)
	}
	defer rt.control.Close()
	if rt.content, err = protocol.ListenContent(protocol.ContentSocketPath(r.Runtime.ControlSocketPath), r.Runtime.ID, r.Runtime.WorkItemID, true); err != nil {
		return fail(err)
	}
	defer rt.content.Close()
	// Each launch unpacks a large runtime into its temporary directory; a
	// private one is removed when the runtime ends.
	tmp, err := os.MkdirTemp("", "wi-agy-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(tmp)
	rt.emitFn = eventEmitter(ctx, r)
	rt.content.Linked = func(requestID string, nativeIDs []string) {
		rt.emit(protocol.RuntimeEvent{Type: protocol.EventPromptSaved, RequestID: requestID, NativeIDs: nativeIDs})
	}
	rt.emit(protocol.RuntimeEvent{Type: protocol.EventRuntimeStatus, Readiness: "starting", ReasonCode: "headless_initializing"})

	cmd := exec.CommandContext(ctx, executable, "acp")
	cmd.Dir = r.Runtime.CWD
	cmd.Env = launchEnvironment(r.Environment, tmp)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fail(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fail(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fail(err)
	}
	rt.stdin = stdin
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	stop := func() {
		_ = stdin.Close()
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-exited
		}
	}
	defer stop()
	rt.conn = acp.NewConn(stdout, stdin, 1024, func(line string) {
		if strings.Contains(line, "authenticate") {
			rt.signInLine = true
		}
	})
	ready, err := rt.start(ctx)
	if err != nil {
		return fail(err)
	}
	replied = true
	if err := reply(ready); err != nil {
		return err
	}
	return rt.loop(ctx, exited)
}

// launchEnvironment is the host's environment for the agent, with wi's
// launch variables, auto-updates off, no browser for sign-in, and a private
// temporary directory.
func launchEnvironment(extra map[string]string, tmp string) []string {
	env := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	for key, value := range extra {
		env[key] = value
	}
	for _, key := range []string{"TMUX", "TMUX_PANE", "TMUX_TMPDIR"} {
		delete(env, key)
	}
	env["AGY_CLI_DISABLE_AUTO_UPDATE"] = "1"
	env["PYTHONUNBUFFERED"] = "1"
	// A runtime never opens a browser to sign in; the user signs in by
	// running agy.
	env["BROWSER"] = "true"
	if tmp != "" {
		env["TMPDIR"] = tmp
	}
	out := make([]string, 0, len(env))
	for key, value := range env {
		out = append(out, key+"="+value)
	}
	sort.Strings(out)
	return out
}

func eventEmitter(ctx context.Context, r protocol.LaunchRequest) func(protocol.RuntimeEvent) {
	sequence := uint64(0)
	resend := protocol.NewStateResender(ctx, r.Runtime.EventSocketPath)
	return func(event protocol.RuntimeEvent) {
		sequence++
		if event.Type == protocol.EventAgentSettled {
			event.Outcome = "completed"
			if event.Failed {
				event.Outcome = "failed"
			}
		}
		event.ProtocolVersion = protocol.RuntimeVersion
		event.EventID = fmt.Sprintf("antigravity:%s:%d", r.Runtime.ID, sequence)
		event.RuntimeID = r.Runtime.ID
		event.WorkItemID = r.Runtime.WorkItemID
		event.Timestamp = time.Now().UTC()
		eventCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		resend.Sent(event, protocol.SubmitEvent(eventCtx, r.Runtime.EventSocketPath, event))
	}
}

// runtime is one running Antigravity session. Its loop owns all state.
type runtime struct {
	request    protocol.LaunchRequest
	config     Config
	conn       *acp.Conn
	stdin      io.WriteCloser
	journal    *journal
	control    *protocol.ControlServer
	content    *protocol.ContentServer
	emitFn     func(protocol.RuntimeEvent)
	signInLine bool

	init     acp.InitializeResult
	session  string
	options  []acp.ConfigOption
	steering bool
	reported map[string]string

	turn     *turn
	queue    []protocol.ControlCommand
	seen     map[string]error
	question *question
	answered string
	sequence int
	prompts  chan promptDone
	status   string
	stopping bool
}

// turn is the agent's work from a prompt, or a wake, until it settles.
type turn struct {
	commandID, requestID string
	// parts are the reply in the order agy wrote it: text, reasoning and
	// tool calls.
	parts []protocol.Part
	// toolIndex finds a call's part by its ID; tools counts the calls.
	toolIndex map[string]int
	tools     int
	// outliving are tools that keep running after their prompt returns,
	// such as commands and timers.
	outliving map[string]bool
	inflight  int
	latest    int
	settled   bool
	stop      string
	failed    bool
	reason    string
	errText   string
	replyOpen bool
	// replyEnded is set when a reply ended with its usage update; the next
	// reply's text starts a new paragraph.
	replyEnded bool
	requests   int
	lastEvent  time.Time
}

// write adds text to the reply: to the text part it ends with, or as a new
// part after reasoning or a call.
func (t *turn) write(text string) {
	if n := len(t.parts); n > 0 && t.parts[n-1].IsText() {
		t.parts[n-1].Text += text
		return
	}
	if text = strings.TrimLeft(text, "\n"); text != "" {
		t.parts = append(t.parts, protocol.TextPart(text))
	}
}

// think adds reasoning to the reply, as write adds text.
func (t *turn) think(text string) {
	if n := len(t.parts); n > 0 && t.parts[n-1].Thought != nil {
		t.parts[n-1].Thought.Text += text
		return
	}
	t.parts = append(t.parts, protocol.ThoughtPart(protocol.Thought{Text: text}))
}

// text is the reply's text parts joined, as live content shows them.
func (t *turn) text() string {
	text := ""
	for _, part := range t.parts {
		if part.IsText() {
			text += part.Text
		}
	}
	return text
}

type promptDone struct {
	seq    int
	result acp.PromptResult
	err    error
}

// question is a fixed-choice question agy asks, held until answered.
type question struct {
	id        json.RawMessage
	toolCall  string
	title     string
	options   []acp.PermissionOption
	sessionID string
	// fields are the questions of an elicitation form; a question asked as
	// a permission request has none.
	fields []formField
}

// formField is one question of an elicitation form: its property, the
// choices by title, and the property for a free-form answer.
type formField struct {
	key, other, title string
	multi             bool
	choices           []formChoice
}

type formChoice struct{ value, title string }

func (rt *runtime) emit(e protocol.RuntimeEvent) { rt.emitFn(e) }

// start initializes ACP and creates or resumes the conversation's session.
func (rt *runtime) start(ctx context.Context) (protocol.Ready, error) {
	initCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	err := rt.conn.Call(initCtx, acp.MethodInitialize, acp.InitializeParams{ProtocolVersion: 1, ClientCapabilities: acp.ClientCapabilities{FS: acp.FileSystemCapabilities{}, Elicitation: &acp.ElicitationCapabilities{Form: struct{}{}}}, ClientInfo: acp.ClientInfo{Name: "wi", Title: "Wi", Version: "1"}}, &rt.init)
	if err != nil {
		return protocol.Ready{}, rt.signInError(fmt.Errorf("Antigravity did not initialize: %w", err))
	}
	version := ""
	if rt.init.AgentInfo != nil {
		version = rt.init.AgentInfo.Version
	}
	rt.steering = versionAtLeast(version, steeringVersion)
	meta := rt.sessionMeta()
	previous, _, err := latestSession(rt.journal.path)
	if err != nil {
		return protocol.Ready{}, err
	}
	var result acp.SessionResult
	switch {
	case previous != "" && rt.init.AgentCapabilities.SessionCapabilities.Resume != nil:
		// Resuming does not replay history, which the journal holds.
		err = rt.conn.Call(initCtx, acp.MethodSessionResume, acp.ResumeSessionParams{SessionID: previous, CWD: rt.request.Runtime.CWD, MCPServers: []any{}, Meta: meta}, &result)
		result.SessionID = previous
	case previous != "":
		return protocol.Ready{}, fmt.Errorf("this Antigravity version cannot resume a session")
	default:
		err = rt.conn.Call(initCtx, acp.MethodSessionNew, acp.NewSessionParams{CWD: rt.request.Runtime.CWD, MCPServers: []any{}, Meta: meta}, &result)
	}
	if err != nil {
		return protocol.Ready{}, rt.signInError(fmt.Errorf("Antigravity session could not start: %w", err))
	}
	if result.SessionID == "" {
		return protocol.Ready{}, fmt.Errorf("Antigravity returned no session")
	}
	rt.session, rt.options = result.SessionID, result.ConfigOptions
	if result.SessionID != previous {
		if err := rt.journal.append(Record{Type: recordSession, ID: "s-" + rt.request.Runtime.ID, SessionID: result.SessionID, CWD: rt.request.Runtime.CWD}); err != nil {
			return protocol.Ready{}, err
		}
	}
	if rt.config.Model != "" {
		if err := rt.setOption(initCtx, protocol.SettingModel, rt.config.Model); err != nil {
			return protocol.Ready{}, err
		}
	}
	if rt.config.Reasoning != "" {
		if err := rt.setOption(initCtx, protocol.SettingReasoning, rt.config.Reasoning); err != nil {
			return protocol.Ready{}, err
		}
	}
	rt.reportSettings()
	rt.prompts = make(chan promptDone, 16)
	rt.emit(protocol.RuntimeEvent{Type: protocol.EventRuntimeReady})
	rt.publishStatus()
	return protocol.Ready{ProtocolVersion: protocol.Version, BackendVersion: version, Capabilities: protocol.Capabilities{
		Headless: true, LiveContent: true, Streaming: true, IdleGuard: true, Prompt: true, FollowUp: true, Cancel: true,
		Steering: rt.steering, ImageInput: rt.init.AgentCapabilities.PromptCapabilities.Image, Control: protocol.ControlStructured,
		Settings: []string{protocol.SettingModel, protocol.SettingReasoning}, Answer: true,
	}}, nil
}

// signInError explains a failure that comes from a missing sign-in.
func (rt *runtime) signInError(err error) error {
	var rpc *acp.Error
	if errors.As(err, &rpc) && (rpc.Code == acp.CodeAuthRequired || strings.HasPrefix(rpc.Message, "No browser is available")) || rt.signInLine {
		return fmt.Errorf("Antigravity is not signed in; run agy once to sign in: %w", err)
	}
	if strings.Contains(err.Error(), "SUBSCRIPTION_REQUIRED") {
		return fmt.Errorf("the Antigravity account has no eligible plan: %w", err)
	}
	return err
}

// deliverables tells agy where the operator can see what it writes: its own
// artifact folder is private to it, and wi shows only the checkout and the
// item's attachments.
const deliverables = `Your artifacts folder (under ~/.gemini) is private to you: the operator cannot see or open anything there. Write a file the operator should read, such as a report, plan or review, into the working directory, or attach it to the work item with ` + "`wi attach FILE`" + `, and refer to it by that path or name.`

// systemPrompt is what wi appends to agy's system prompt: the operator's
// instructions, the skills wi gives every agent, which ACP has no other way
// to pass, and where deliverables go.
func (rt *runtime) systemPrompt() string {
	parts := []string{}
	if text := strings.TrimSpace(rt.request.AppendSystemPrompt); text != "" {
		parts = append(parts, text)
	}
	skills := rt.request.Skills
	if protocol.ValidateSkills(skills) != nil {
		skills = nil
	}
	for _, skill := range skills {
		for _, file := range skill.Files {
			if file.Path == "SKILL.md" {
				parts = append(parts, fmt.Sprintf("# Skill: %s\n\n%s", skill.Name, strings.TrimSpace(string(file.Data))))
			}
		}
	}
	return strings.Join(append(parts, deliverables), "\n\n")
}

// sessionMeta asks agy to append wi's instructions to its system prompt,
// when it says it can.
func (rt *runtime) sessionMeta() json.RawMessage {
	text := rt.systemPrompt()
	var meta struct {
		Antigravity struct {
			SystemPrompt struct {
				Append bool `json:"append"`
			} `json:"systemPrompt"`
		} `json:"antigravity"`
	}
	if json.Unmarshal(rt.init.AgentCapabilities.Meta, &meta) != nil || !meta.Antigravity.SystemPrompt.Append {
		return nil
	}
	data, _ := json.Marshal(map[string]any{"antigravity": map[string]any{"systemPrompt": map[string]any{"append": text}}})
	return data
}

// option finds a session setting by wi name: the model, or the thinking
// level as reasoning.
func (rt *runtime) option(setting string) *acp.ConfigOption {
	for i := range rt.options {
		o := &rt.options[i]
		switch {
		case setting == protocol.SettingModel && (o.Category == "model" || o.ID == "model"):
			return o
		case setting == protocol.SettingReasoning && (o.Category == "thought_level" || o.ID == "thinking_level"):
			return o
		}
	}
	return nil
}

// setOption changes a setting through the session's config options.
func (rt *runtime) setOption(ctx context.Context, setting, value string) error {
	o := rt.option(setting)
	if o == nil {
		return fmt.Errorf("this Antigravity session has no %s setting", setting)
	}
	if setting == protocol.SettingReasoning {
		value = reasoningValue(acp.Values(o.Options), value)
	} else if !hasChoice(acp.Values(o.Options), value) && len(o.Options) > 0 {
		// agy names a model with its thinking level, as in
		// gemini-4-neon-high; a session sets them apart.
		base, level, _ := splitLevel(value, "")
		if level == "" || !hasChoice(acp.Values(o.Options), base) {
			return fmt.Errorf("Antigravity model %q is not available to this account", value)
		}
		if err := rt.setOption(ctx, protocol.SettingModel, base); err != nil {
			return err
		}
		return rt.setOption(ctx, protocol.SettingReasoning, level)
	}
	if o.CurrentValue == value {
		return nil
	}
	var result acp.SetConfigOptionResult
	if err := rt.conn.Call(ctx, acp.MethodSetConfigOption, acp.SetConfigOptionParams{SessionID: rt.session, ConfigID: o.ID, Value: value}, &result); err != nil {
		return fmt.Errorf("set Antigravity %s: %w", setting, err)
	}
	if len(result.ConfigOptions) > 0 {
		rt.options = result.ConfigOptions
	} else {
		o.CurrentValue = value
	}
	return nil
}

func hasChoice(choices []acp.ConfigChoice, value string) bool {
	for _, c := range choices {
		if c.Value == value {
			return true
		}
	}
	return false
}

// reasoningLadder is wi's reasoning scale, deepest last.
var reasoningLadder = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// reasoningValue maps a wi reasoning level onto the model's thinking
// levels: the level itself, or the nearest one below it, or the lowest.
func reasoningValue(choices []acp.ConfigChoice, level string) string {
	if hasChoice(choices, level) || len(choices) == 0 {
		return level
	}
	rank := func(v string) int {
		for i, l := range reasoningLadder {
			if l == v {
				return i
			}
		}
		return -1
	}
	want, best, bestRank := rank(level), "", -1
	for _, c := range choices {
		if r := rank(c.Value); r >= 0 && r <= want && r > bestRank {
			best, bestRank = c.Value, r
		}
	}
	if best == "" {
		best = choices[0].Value
	}
	return best
}

// reportSettings reports the model and reasoning level the session runs
// with, when they changed.
func (rt *runtime) reportSettings() {
	current := map[string]string{}
	for _, setting := range []string{protocol.SettingModel, protocol.SettingReasoning} {
		if o := rt.option(setting); o != nil && o.CurrentValue != "" {
			current[setting] = o.CurrentValue
		}
	}
	changed := map[string]string{}
	for key, value := range current {
		if rt.reported[key] != value {
			changed[key] = value
		}
	}
	rt.reported = current
	if len(changed) > 0 {
		rt.emit(protocol.RuntimeEvent{Type: protocol.EventSettingsChanged, Settings: changed})
	}
}

var versionPattern = regexp.MustCompile(`^(?:agy_acp_server_)?(\d+)\.(\d+)\.(\d+)`)

// versionAtLeast compares agy release versions; unknown versions are older.
func versionAtLeast(version, minimum string) bool {
	parse := func(v string) []int {
		m := versionPattern.FindStringSubmatch(v)
		if m == nil {
			return nil
		}
		out := make([]int, 3)
		for i := range out {
			out[i], _ = strconv.Atoi(m[i+1])
		}
		return out
	}
	have, want := parse(version), parse(minimum)
	if have == nil {
		return false
	}
	for i := range want {
		if have[i] != want[i] {
			return have[i] > want[i]
		}
	}
	return true
}

// loop serves controls and the agent until the runtime stops.
func (rt *runtime) loop(ctx context.Context, exited <-chan error) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-exited:
			if rt.stopping {
				return nil
			}
			rt.emit(protocol.RuntimeEvent{Type: protocol.EventRuntimeFailed, Failed: true, ReasonCode: "runtime_stopped"})
			return fmt.Errorf("Antigravity exited: %v", err)
		case <-rt.conn.Done():
			if rt.stopping {
				return nil
			}
			rt.emit(protocol.RuntimeEvent{Type: protocol.EventRuntimeFailed, Failed: true, ReasonCode: "headless_protocol_error"})
			return fmt.Errorf("Antigravity connection ended: %v", rt.conn.Err())
		case request := <-rt.control.Requests():
			c := request.Command
			if c.ProtocolVersion != protocol.RuntimeVersion || c.RuntimeID != rt.request.Runtime.ID || c.WorkItemID != rt.request.Runtime.WorkItemID {
				request.Respond(fmt.Errorf("runtime identity mismatch"))
				continue
			}
			err := rt.command(ctx, c)
			request.Respond(err)
			if c.Type == protocol.CommandShutdown && err == nil {
				return nil
			}
		case in, ok := <-rt.conn.Incoming():
			if ok {
				rt.incoming(in)
			}
		case done := <-rt.prompts:
			// The agent sends a prompt's updates before its response, and
			// they queue first: apply them before ending the prompt.
			rt.drain()
			rt.promptDone(done)
		case <-ticker.C:
			rt.maybeSettle(time.Now())
		}
		rt.publishStatus()
	}
}

// drain applies the agent's queued messages.
func (rt *runtime) drain() {
	for {
		select {
		case in, ok := <-rt.conn.Incoming():
			if !ok {
				return
			}
			rt.incoming(in)
		default:
			return
		}
	}
}

// publishStatus reports the runtime's readiness when it changes.
func (rt *runtime) publishStatus() {
	readiness, reason := "ready", "headless_ready"
	switch {
	case rt.stopping:
		readiness, reason = "stopping", "runtime_stopping"
	case rt.question != nil:
		readiness, reason = "waiting_input", "question_pending"
	case rt.turn != nil:
		readiness, reason = "busy", "active_turn"
	}
	if key := readiness + ":" + reason; key != rt.status {
		rt.status = key
		rt.emit(protocol.RuntimeEvent{Type: protocol.EventRuntimeStatus, Readiness: readiness, ReasonCode: reason})
	}
}

// command applies one wi control.
func (rt *runtime) command(ctx context.Context, c protocol.ControlCommand) error {
	if err := c.ValidateContent(); err != nil {
		return err
	}
	if c.ID == "" || len(c.ID) > 200 || len(c.RequestID) > 200 {
		return fmt.Errorf("bounded command identity required")
	}
	if err, ok := rt.seen[c.ID]; ok && c.Type != protocol.CommandShutdown {
		return err
	}
	if len(rt.seen) >= 1024 {
		return fmt.Errorf("Antigravity command deduplication capacity reached; restart the runtime")
	}
	err := rt.apply(ctx, c)
	rt.seen[c.ID] = err
	return err
}

func (rt *runtime) apply(ctx context.Context, c protocol.ControlCommand) error {
	if rt.stopping {
		return fmt.Errorf("Antigravity is stopping")
	}
	switch c.Type {
	case protocol.CommandShutdown:
		if c.RequireIdle && rt.turn != nil {
			return protocol.NotIdle("Antigravity is busy")
		}
		rt.stopping = true
		rt.emit(protocol.RuntimeEvent{Type: protocol.EventRuntimeStopping})
		return nil
	case protocol.CommandAbort:
		return rt.abort(c)
	case protocol.CommandSet:
		setCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := rt.setOption(setCtx, c.Setting.Name, c.Setting.Value); err != nil {
			return err
		}
		rt.reportSettings()
		rt.emit(protocol.RuntimeEvent{Type: protocol.EventCommandAccepted, CommandID: c.ID, RequestID: c.RequestID, ReasonCode: protocol.ReasonSettingApplied})
		return nil
	case protocol.CommandAnswer:
		return rt.answer(c)
	case protocol.CommandPrompt, protocol.CommandSteer, protocol.CommandFollowUp:
	default:
		return fmt.Errorf("unsupported Antigravity control %q", c.Type)
	}
	if strings.TrimSpace(c.Message) == "" && len(c.Images) == 0 {
		return fmt.Errorf("text or images required")
	}
	if len(c.Images) > 0 && !rt.init.AgentCapabilities.PromptCapabilities.Image {
		return fmt.Errorf("this Antigravity version takes no images")
	}
	busy := rt.turn != nil
	if c.RequireIdle && busy {
		return protocol.NotIdle("Antigravity is busy")
	}
	switch {
	case !busy:
		return rt.prompt(c)
	case c.Type == protocol.CommandFollowUp || c.Type == protocol.CommandSteer && !rt.steering:
		if len(rt.queue) >= maxQueue {
			return fmt.Errorf("Antigravity follow-up queue is full")
		}
		rt.queue = append(rt.queue, c)
		rt.emit(protocol.RuntimeEvent{Type: protocol.EventQueueChanged, CommandID: c.ID, RequestID: c.RequestID, DeliveryState: "queued"})
		return nil
	case c.Type == protocol.CommandSteer:
		// agy reads a prompt sent during a turn as steering: the running
		// prompt ends and the turn carries on under the new one.
		return rt.prompt(c)
	default:
		return fmt.Errorf("Antigravity is busy; use steer or follow_up")
	}
}

// prompt records a message and sends it to the agent.
func (rt *runtime) prompt(c protocol.ControlCommand) error {
	rt.sequence++
	id := fmt.Sprintf("u-%s-%d", rt.request.Runtime.ID, rt.sequence)
	blocks := []acp.ContentBlock{}
	images := []protocol.HistoryImage{}
	if text := strings.TrimSpace(c.Message); text != "" {
		blocks = append(blocks, acp.ContentBlock{Type: "text", Text: text})
	}
	for _, image := range c.Images {
		blocks = append(blocks, acp.ContentBlock{Type: "image", Data: image.Data, MIMEType: image.MIMEType})
		images = append(images, protocol.HistoryImage{MIMEType: image.MIMEType, Data: image.Data})
	}
	if err := rt.journal.append(Record{Type: recordUser, ID: id, RequestID: c.RequestID, Text: strings.TrimSpace(c.Message), Images: images}); err != nil {
		return err
	}
	ch, requestID, err := rt.conn.Send(acp.MethodSessionPrompt, acp.PromptParams{SessionID: rt.session, Prompt: blocks})
	if err != nil {
		return fmt.Errorf("Antigravity prompt delivery failed: %w", err)
	}
	seq := rt.sequence
	go func() {
		var result acp.PromptResult
		err := rt.conn.Wait(context.Background(), ch, requestID, &result)
		rt.prompts <- promptDone{seq: seq, result: result, err: err}
	}()
	if rt.turn == nil {
		rt.begin(c.ID, c.RequestID)
	} else {
		rt.turn.commandID, rt.turn.requestID = c.ID, c.RequestID
	}
	rt.turn.inflight++
	rt.turn.latest, rt.turn.settled = seq, false
	rt.content.PromptSaved(c.RequestID, []string{id})
	rt.emit(protocol.RuntimeEvent{Type: protocol.EventCommandAccepted, CommandID: c.ID, RequestID: c.RequestID, DeliveryState: "submitted"})
	return nil
}

// begin opens a turn, for a prompt or for work agy starts by itself.
func (rt *runtime) begin(commandID, requestID string) {
	rt.turn = &turn{commandID: commandID, requestID: requestID, toolIndex: map[string]int{}, outliving: map[string]bool{}, lastEvent: time.Now()}
	rt.content.StartMessage()
	rt.content.SetActivity("Working")
	rt.emit(protocol.RuntimeEvent{Type: protocol.EventAgentStarted})
}

func (rt *runtime) abort(c protocol.ControlCommand) error {
	rt.queue = nil
	if rt.turn == nil {
		rt.emit(protocol.RuntimeEvent{Type: protocol.EventCommandAccepted, CommandID: c.ID, RequestID: c.RequestID, ReasonCode: protocol.ReasonAbortAccepted})
		return nil
	}
	// Cancelling also ends the commands and subagents the agent runs.
	if err := rt.conn.Notify(acp.MethodSessionCancel, acp.CancelParams{SessionID: rt.session}); err != nil {
		return fmt.Errorf("Antigravity cancel delivery failed: %w", err)
	}
	rt.cancelQuestion()
	rt.turn.stop = acp.StopCancelled
	rt.turn.outliving = map[string]bool{}
	rt.emit(protocol.RuntimeEvent{Type: protocol.EventCommandAccepted, CommandID: c.ID, RequestID: c.RequestID, ReasonCode: protocol.ReasonAbortAccepted})
	return nil
}

// promptDone records a prompt's end. Only the latest prompt of a steered
// turn ends it; failures end it at once.
func (rt *runtime) promptDone(done promptDone) {
	t := rt.turn
	if t == nil {
		return
	}
	t.inflight--
	t.lastEvent = time.Now()
	if done.err != nil {
		t.failed, t.errText = true, rt.signInError(done.err).Error()
		t.outliving = map[string]bool{}
		rt.settle()
		return
	}
	if done.seq != t.latest {
		return
	}
	t.settled = true
	if t.stop != acp.StopCancelled {
		t.stop = done.result.StopReason
	}
	switch done.result.StopReason {
	case acp.StopRefusal:
		// agy reports an exhausted quota as a refusal.
		t.failed, t.reason = true, "usage_limit"
		t.outliving = map[string]bool{}
	case acp.StopMaxTokens, acp.StopMaxTurnRequests:
		t.failed = true
	case acp.StopCancelled:
		t.outliving = map[string]bool{}
	}
	if !rt.backgroundWork() {
		rt.settle()
	}
}

// backgroundWork reports whether the turn still waits on work that
// outlives its prompt: a command or timer, or a question.
func (rt *runtime) backgroundWork() bool {
	t := rt.turn
	return t.inflight > 0 || len(t.outliving) > 0 || t.requests > 0 || rt.question != nil
}

// maybeSettle settles a turn whose background work ended and that stayed
// quiet long enough for a late reply.
func (rt *runtime) maybeSettle(now time.Time) {
	t := rt.turn
	if t == nil || !t.settled || rt.backgroundWork() {
		return
	}
	wait := quietSettle
	if t.replyOpen {
		wait = openReplySettle
	}
	if now.Sub(t.lastEvent) >= wait {
		rt.settle()
	}
}

// settle ends the turn: its reply goes to the journal, and the next queued
// follow-up starts.
func (rt *runtime) settle() {
	t := rt.turn
	if t == nil {
		return
	}
	rt.turn = nil
	rt.sequence++
	id := fmt.Sprintf("a-%s-%d", rt.request.Runtime.ID, rt.sequence)
	record := Record{Type: recordAssistant, ID: id, Parts: t.parts, StopReason: t.stop, Failed: t.failed, Reason: t.reason, Error: t.errText}
	if record.StopReason == "" {
		record.StopReason = acp.StopEndTurn
	}
	if err := rt.journal.append(record); err != nil {
		fmt.Fprintf(os.Stderr, "Antigravity journal write failed: %v\n", err)
	}
	rt.content.MarkSaved(t.text(), []string{id})
	rt.content.SetActivity("Idle")
	rt.content.Update(protocol.EventAgentSettled, "", "")
	rt.emit(protocol.RuntimeEvent{Type: protocol.EventAgentSettled, Failed: t.failed, ReasonCode: t.reason, UsageProvider: reasonUsageProvider(t.reason), CommandID: t.commandID, RequestID: t.requestID})
	if len(rt.queue) > 0 {
		next := rt.queue[0]
		rt.queue = rt.queue[1:]
		if err := rt.prompt(next); err != nil {
			rt.seen[next.ID] = err
			rt.emit(protocol.RuntimeEvent{Type: protocol.EventCommandRejected, CommandID: next.ID, RequestID: next.RequestID, Failed: true, ReasonCode: "delivery_failed"})
		}
	}
}

// incoming handles a request or notification from the agent.
func (rt *runtime) incoming(in acp.Incoming) {
	switch in.Method {
	case acp.MethodSessionUpdate:
		var params acp.SessionUpdateParams
		if json.Unmarshal(in.Params, &params) != nil || params.SessionID != rt.session {
			return
		}
		rt.update(params.Update)
	case acp.MethodElicitation:
		rt.elicitation(in.ID, in.Params)
	case acp.MethodRequestPermission:
		var params acp.RequestPermissionParams
		if json.Unmarshal(in.Params, &params) != nil || params.SessionID != rt.session {
			_ = rt.conn.Respond(in.ID, acp.Cancelled(), nil)
			return
		}
		rt.permission(in.ID, params)
	default:
		if in.IsRequest() {
			_ = rt.conn.Respond(in.ID, nil, &acp.Error{Code: acp.CodeMethodNotFound, Message: "wi does not provide " + in.Method})
		}
	}
}

// update applies a session update to the turn and live content. Output
// with no turn open is the agent waking by itself, for example when a
// background command ends.
func (rt *runtime) update(u acp.SessionUpdate) {
	switch u.Kind {
	case acp.UpdateConfigOption:
		if len(u.ConfigOptions) > 0 {
			rt.options = u.ConfigOptions
			rt.reportSettings()
		}
		return
	case acp.UpdateUsage:
		if rt.turn != nil {
			rt.turn.replyOpen, rt.turn.replyEnded = false, true
			rt.turn.lastEvent = time.Now()
		}
		return
	case acp.UpdateAgentMessage, acp.UpdateAgentThought, acp.UpdateToolCall, acp.UpdateToolCallPatch:
	default:
		return
	}
	if rt.turn == nil {
		rt.begin("", "")
		rt.turn.settled = true
	}
	t := rt.turn
	t.replyOpen, t.lastEvent = true, time.Now()
	switch u.Kind {
	case acp.UpdateAgentMessage:
		if text := blockText(u.Content); text != "" {
			// A later reply of the same turn, such as after a background
			// command, starts a paragraph of its own.
			if t.replyEnded && t.text() != "" && !strings.HasPrefix(text, "\n") {
				text = "\n\n" + text
			}
			t.replyEnded = false
			t.write(text)
			rt.content.Update(protocol.EventMessageDelta, text, "")
		}
	case acp.UpdateAgentThought:
		if text := blockText(u.Content); text != "" {
			t.think(text)
			rt.content.Thought(text)
		}
	case acp.UpdateToolCall, acp.UpdateToolCallPatch:
		rt.tool(u)
	}
}

// tool records a tool call or its update.
func (rt *runtime) tool(u acp.SessionUpdate) {
	t := rt.turn
	if u.ToolCallID == "" {
		return
	}
	i, known := t.toolIndex[u.ToolCallID]
	if !known {
		if t.tools >= 256 {
			return
		}
		t.tools++
		t.parts = append(t.parts, protocol.ToolPart(protocol.HistoryTool{ID: u.ToolCallID, Status: "running"}))
		i = len(t.parts) - 1
		t.toolIndex[u.ToolCallID] = i
	}
	tool := t.parts[i].Tool
	if u.Name != nil && *u.Name != "" {
		tool.Name = *u.Name
	} else if u.Title != nil && tool.Name == "" {
		tool.Name = *u.Title
	}
	if tool.Name == "" {
		tool.Name = "tool"
	}
	if len(u.RawInput) > 0 && string(u.RawInput) != "null" {
		tool.Input = bounded(string(u.RawInput))
	}
	if output := toolOutput(u); output != "" {
		tool.Output = bounded(output)
	}
	status := ""
	if u.Status != nil {
		status = *u.Status
	}
	kind := ""
	if u.ToolKind != nil {
		kind = *u.ToolKind
	}
	switch status {
	case acp.ToolCompleted:
		tool.Status = "completed"
		delete(t.outliving, u.ToolCallID)
	case acp.ToolFailed:
		tool.Status = "failed"
		delete(t.outliving, u.ToolCallID)
	case acp.ToolInProgress, acp.ToolPending, "":
		if outlivesPrompt(kind, title(u, tool.Name), u.RawInput) {
			t.outliving[u.ToolCallID] = true
		}
	}
	event := protocol.EventToolUpdated
	switch {
	case !known:
		event = protocol.EventToolStarted
		rt.emit(protocol.RuntimeEvent{Type: protocol.EventToolStarted, ToolName: compactName(tool.Name)})
	case tool.Status != "running":
		event = protocol.EventToolCompleted
		rt.emit(protocol.RuntimeEvent{Type: protocol.EventToolCompleted, ToolName: compactName(tool.Name), Failed: tool.Status == "failed"})
	}
	rt.content.UpdateTool(event, "", tool.ID, tool.Name, tool.Input, tool.Output, tool.Status == "failed")
}

func title(u acp.SessionUpdate, fallback string) string {
	if u.Title != nil {
		return *u.Title
	}
	return fallback
}

// outlivesPrompt reports a tool agy keeps running after its prompt
// returns: a command it waits for (not a daemon), or a scheduled wake.
func outlivesPrompt(kind, title string, rawInput json.RawMessage) bool {
	if title == "Run schedule" || title == "Running schedule" {
		return true
	}
	if kind != "execute" {
		return false
	}
	var input struct {
		IsDaemon bool `json:"IsDaemon"`
	}
	_ = json.Unmarshal(rawInput, &input)
	return !input.IsDaemon
}

func compactName(name string) string {
	if len(name) > 128 {
		return name[:128]
	}
	return name
}

// blockText is the text of a content block.
func blockText(raw json.RawMessage) string {
	var block acp.ContentBlock
	if json.Unmarshal(raw, &block) != nil || block.Type != "text" {
		return ""
	}
	return block.Text
}

// toolOutput is a tool's result: its text content, diffs, or raw output.
func toolOutput(u acp.SessionUpdate) string {
	var parts []string
	var content []acp.ToolCallContent
	if json.Unmarshal(u.Content, &content) == nil {
		for _, c := range content {
			switch {
			case c.Type == "content" && c.Content != nil && c.Content.Type == "text":
				parts = append(parts, c.Content.Text)
			case c.Type == "diff":
				parts = append(parts, "--- "+c.Path+"\n+++ "+c.Path+"\n"+c.NewText)
			}
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	if len(u.RawOutput) > 0 && string(u.RawOutput) != "null" {
		var text string
		if json.Unmarshal(u.RawOutput, &text) == nil {
			return text
		}
		return string(u.RawOutput)
	}
	return ""
}

// bounded keeps a tool field within live content's limits.
func bounded(value string) string {
	const limit = 16 << 10
	if len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && value[end]&0xc0 == 0x80 {
		end--
	}
	return value[:end] + "\n[truncated]"
}

// permission answers a permission request. wi runs agents with full
// access, so tools are allowed once each; a fixed-choice question waits
// for the operator's answer.
func (rt *runtime) permission(id json.RawMessage, params acp.RequestPermissionParams) {
	if rt.turn == nil {
		rt.begin("", "")
		rt.turn.settled = true
	}
	rt.turn.lastEvent, rt.turn.replyOpen = time.Now(), true
	if strings.HasPrefix(params.ToolCall.ToolCallID, questionPrefix) && len(params.Options) > 0 {
		rt.cancelQuestion()
		text := ""
		if params.ToolCall.Title != nil {
			text = *params.ToolCall.Title
		}
		if text == "" {
			text = "The agent asks"
		}
		rt.question = &question{id: id, toolCall: params.ToolCall.ToolCallID, title: text, options: params.Options, sessionID: params.SessionID}
		rt.content.SetQuestion(params.ToolCall.ToolCallID, "question", questionInput(text, params.Options))
		return
	}
	for _, kind := range []string{acp.AllowOnce, acp.AllowAlways} {
		for _, option := range params.Options {
			if option.Kind == kind {
				_ = rt.conn.Respond(id, acp.Selected(option.OptionID), nil)
				return
			}
		}
	}
	_ = rt.conn.Respond(id, acp.Cancelled(), nil)
}

// questionInput presents a question in the shape wi's clients read: one
// question, its choices as options.
func questionInput(text string, options []acp.PermissionOption) string {
	choices := []map[string]string{}
	for _, option := range options {
		choices = append(choices, map[string]string{"label": option.Name})
	}
	data, _ := json.Marshal(map[string]any{"questions": []any{map[string]any{"question": text, "multiSelect": false, "options": choices}}})
	return string(data)
}

// answer answers the pending question with the chosen option.
func (rt *runtime) answer(c protocol.ControlCommand) error {
	q := rt.question
	if q == nil && c.ToolCallID != "" && c.ToolCallID == rt.answered {
		return nil
	}
	if q == nil {
		return fmt.Errorf("Antigravity is not waiting for an answer")
	}
	if c.ToolCallID != "" && c.ToolCallID != q.toolCall {
		return fmt.Errorf("Antigravity is waiting on another question")
	}
	if q.fields != nil {
		return rt.answerForm(q, c.Answers)
	}
	chosen, ok := c.Answers[q.title]
	if !ok || len(c.Answers) != 1 {
		return fmt.Errorf("answer Antigravity's question %q", q.title)
	}
	for _, option := range q.options {
		if strings.EqualFold(strings.TrimSpace(option.Name), strings.TrimSpace(chosen)) {
			if err := rt.conn.Respond(q.id, acp.Selected(option.OptionID), nil); err != nil {
				return fmt.Errorf("Antigravity answer delivery failed: %w", err)
			}
			rt.answered = q.toolCall
			rt.dropQuestion()
			if rt.turn != nil {
				rt.turn.lastEvent, rt.turn.replyOpen = time.Now(), true
			}
			return nil
		}
	}
	return fmt.Errorf("Antigravity offers only fixed choices: %s", choiceNames(q.options))
}

func choiceNames(options []acp.PermissionOption) string {
	names := []string{}
	for _, option := range options {
		names = append(names, strconv.Quote(option.Name))
	}
	return strings.Join(names, ", ")
}

func (rt *runtime) dropQuestion() {
	rt.question = nil
	rt.content.SetQuestion("", "", "")
}

// cancelQuestion withdraws the pending question, unanswered.
func (rt *runtime) cancelQuestion() {
	q := rt.question
	if q == nil {
		return
	}
	if q.fields != nil {
		_ = rt.conn.Respond(q.id, map[string]string{"action": "cancel"}, nil)
	} else {
		_ = rt.conn.Respond(q.id, acp.Cancelled(), nil)
	}
	rt.dropQuestion()
}

// elicitation holds a question form agy sends for its ask_question tool:
// each question is a property whose choices are oneOf (one) or
// items.anyOf (several) constants with titles, and a matching _other
// property takes a free-form answer.
func (rt *runtime) elicitation(id json.RawMessage, params json.RawMessage) {
	var request struct {
		SessionID       string `json:"sessionId"`
		ToolCallID      string `json:"toolCallId"`
		Mode            string `json:"mode"`
		RequestedSchema struct {
			Properties map[string]struct {
				Type  string `json:"type"`
				Title string `json:"title"`
				OneOf []struct {
					Const string `json:"const"`
					Title string `json:"title"`
				} `json:"oneOf"`
				Items struct {
					AnyOf []struct {
						Const string `json:"const"`
						Title string `json:"title"`
					} `json:"anyOf"`
				} `json:"items"`
			} `json:"properties"`
		} `json:"requestedSchema"`
	}
	if json.Unmarshal(params, &request) != nil || request.SessionID != rt.session || request.Mode != "form" {
		_ = rt.conn.Respond(id, map[string]string{"action": "decline"}, nil)
		return
	}
	keys := make([]string, 0, len(request.RequestedSchema.Properties))
	for key := range request.RequestedSchema.Properties {
		if !strings.HasSuffix(key, "_other") {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.TrimPrefix(keys[i], "q"))
		b, _ := strconv.Atoi(strings.TrimPrefix(keys[j], "q"))
		if a != b {
			return a < b
		}
		return keys[i] < keys[j]
	})
	var fields []formField
	questions := []any{}
	for _, key := range keys {
		property := request.RequestedSchema.Properties[key]
		field := formField{key: key, title: property.Title, multi: property.Type == "array"}
		if _, ok := request.RequestedSchema.Properties[key+"_other"]; ok {
			field.other = key + "_other"
		}
		for _, c := range property.OneOf {
			field.choices = append(field.choices, formChoice{value: c.Const, title: c.Title})
		}
		for _, c := range property.Items.AnyOf {
			field.choices = append(field.choices, formChoice{value: c.Const, title: c.Title})
		}
		if field.title == "" {
			field.title = key
		}
		options := []map[string]string{}
		for _, c := range field.choices {
			options = append(options, map[string]string{"label": c.title})
		}
		questions = append(questions, map[string]any{"question": field.title, "multiSelect": field.multi, "options": options})
		fields = append(fields, field)
	}
	if len(fields) == 0 {
		_ = rt.conn.Respond(id, map[string]string{"action": "decline"}, nil)
		return
	}
	if rt.turn == nil {
		rt.begin("", "")
		rt.turn.settled = true
	}
	rt.turn.lastEvent, rt.turn.replyOpen = time.Now(), true
	rt.cancelQuestion()
	toolCall := request.ToolCallID
	if toolCall == "" {
		toolCall = "elicitation-" + string(id)
	}
	rt.question = &question{id: id, toolCall: toolCall, sessionID: request.SessionID, fields: fields}
	input, _ := json.Marshal(map[string]any{"questions": questions})
	rt.content.SetQuestion(toolCall, "question", string(input))
}

// answerForm answers an elicitation form: each answer is a choice title,
// titles joined by ", " for several, or the operator's own text.
func (rt *runtime) answerForm(q *question, answers map[string]string) error {
	content := map[string]any{}
	for _, field := range q.fields {
		answer, ok := answers[field.title]
		if !ok {
			return fmt.Errorf("answer each of Antigravity's %d questions", len(q.fields))
		}
		pick := func(text string) (string, bool) {
			for _, c := range field.choices {
				if strings.EqualFold(strings.TrimSpace(c.title), strings.TrimSpace(text)) {
					return c.value, true
				}
			}
			return "", false
		}
		if value, ok := pick(answer); ok {
			if field.multi {
				content[field.key] = []string{value}
			} else {
				content[field.key] = value
			}
			continue
		}
		if field.multi {
			values, own := []string{}, []string{}
			for _, part := range strings.Split(answer, ",") {
				if value, ok := pick(part); ok {
					values = append(values, value)
				} else if strings.TrimSpace(part) != "" {
					own = append(own, strings.TrimSpace(part))
				}
			}
			if len(values) > 0 {
				content[field.key] = values
			}
			if len(own) > 0 {
				if field.other == "" {
					return fmt.Errorf("Antigravity offers only fixed choices for %q", field.title)
				}
				content[field.other] = strings.Join(own, ", ")
			}
			continue
		}
		if field.other == "" {
			return fmt.Errorf("Antigravity offers only fixed choices for %q", field.title)
		}
		content[field.other] = answer
	}
	if err := rt.conn.Respond(q.id, map[string]any{"action": "accept", "content": content}, nil); err != nil {
		return fmt.Errorf("Antigravity answer delivery failed: %w", err)
	}
	rt.answered = q.toolCall
	rt.dropQuestion()
	if rt.turn != nil {
		rt.turn.lastEvent, rt.turn.replyOpen = time.Now(), true
	}
	return nil
}
