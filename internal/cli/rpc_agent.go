package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func rpcWorkspace(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{errors.New("workspace requires status, ensure, setup or release")}
	}
	fs := newFlagSet("workspace "+args[0], cfg.Stderr)
	selected := fs.String("item", "", "item")
	skip := fs.Bool("skip-setup", false, "skip setup")
	if err := rpcParse(fs, args[1:]); err != nil {
		return err
	}
	selector, err := itemSelectorFromFlag(fs, *selected)
	if err != nil {
		return usageErr{err}
	}
	id, err := rpcSelect(ctx, cfg, selector)
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		value, err := cfg.API.Workspaces.GetWorkspace(ctx, rpcRequest(cfg, &v1.GetWorkspaceRequest{ItemId: id}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "ensure":
		value, err := cfg.API.Workspaces.EnsureWorkspace(ctx, rpcRequest(cfg, &v1.EnsureWorkspaceRequest{ItemId: id, SkipSetup: *skip}))
		if err != nil {
			return err
		}
		value.Msg.Job, err = rpcWaitJob(ctx, cfg, value.Msg.Job, jsonOut)
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "setup":
		value, err := cfg.API.Workspaces.SetupWorkspace(ctx, rpcRequest(cfg, &v1.SetupWorkspaceRequest{ItemId: id}))
		if err != nil {
			return err
		}
		value.Msg.Job, err = rpcWaitJob(ctx, cfg, value.Msg.Job, jsonOut)
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "release":
		value, err := cfg.API.Workspaces.ReleaseWorkspace(ctx, rpcRequest(cfg, &v1.ReleaseWorkspaceRequest{ItemId: id}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "commit":
		value, err := cfg.API.Workspaces.CommitWorkspace(ctx, rpcRequest(cfg, &v1.CommitWorkspaceRequest{ItemId: id}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	return usageErr{fmt.Errorf("unknown workspace command %q", args[0])}
}
func rpcLifecycle(ctx context.Context, command string, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet(command, cfg.Stderr)
	selected := fs.String("item", "", "item")
	mode := fs.String("agent-mode", "tui", "agent mode")
	force := fs.Bool("force", false, "force busy stop")
	skip := fs.Bool("skip-setup", false, "skip setup")
	resolution := fs.String("resolution", "completed", "resolution")
	commit := fs.Bool("commit-changes", false, "commit changes")
	backlog := fs.Bool("backlog", false, "reopen without starting")
	prompt := fs.String("prompt", "", "startup message")
	noSend := fs.Bool("send-nothing", false, "start without sending a message")
	fs.BoolVar(noSend, "no-send", false, "start without sending a message")
	confirmed := fs.Bool("confirm-attached-terminal", false, "confirm disconnecting terminal clients")
	modelName := fs.String("model", "", "model")
	reasoning := fs.String("reasoning", "", "reasoning")
	noAttach := fs.Bool("no-attach", false, "do not attach")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	if command == "reopen" && *backlog && (*force || *skip || *modelName != "" || *reasoning != "") {
		return usageErr{errors.New("--force, --skip-setup, --model and --reasoning do not combine with --backlog")}
	}
	selector, err := itemSelectorFromFlag(fs, *selected)
	if err != nil {
		return usageErr{err}
	}
	id, err := rpcSelect(ctx, cfg, selector)
	if err != nil {
		return err
	}
	wanted, err := rpcMode(*mode)
	if err != nil {
		return err
	}
	if settings := agentSettingFlags(*modelName, *reasoning); len(settings) > 0 && command != "reopen" {
		if _, err := cfg.API.Agents.UpdateAgentSettings(ctx, rpcRequest(cfg, &v1.UpdateAgentSettingsRequest{ItemId: id, Settings: settings})); err != nil {
			return err
		}
	}
	switch command {
	case "start", "resume":
		value, err := cfg.API.Lifecycle.StartItem(ctx, rpcRequest(cfg, &v1.StartItemRequest{ItemId: id, Start: &v1.StartOptions{Mode: wanted, Prompt: *prompt, SendNothing: *noSend || *prompt == "", SkipSetup: *skip, OverrideCapacity: *force, ClientMessageId: newCommandID()}}))
		if err != nil {
			return err
		}
		value.Msg.Job, err = rpcWaitJob(ctx, cfg, value.Msg.Job, jsonOut)
		if err != nil {
			return err
		}
		value.Msg.Item, err = rpcCurrentItem(ctx, cfg, id)
		if err != nil {
			return err
		}
		if err := rpcEmit(cfg, jsonOut, value.Msg); err != nil {
			return err
		}
		if !jsonOut && !*noAttach && wanted == v1.AgentMode_AGENT_MODE_TUI && interactiveCLI(cfg) {
			return rpcEnterTerminal(ctx, cfg, id, false, false)
		}
		return nil
	case "shelve":
		if *force {
			return usageErr{errors.New("ShelveItem refuses busy agents; stop the agent with --force first")}
		}
		value, err := cfg.API.Lifecycle.ShelveItem(ctx, rpcRequest(cfg, &v1.ShelveItemRequest{ItemId: id, ConfirmAttachedTerminal: *confirmed}))
		if err != nil {
			return err
		}
		value.Msg.Job, err = rpcWaitJob(ctx, cfg, value.Msg.Job, jsonOut)
		if err != nil {
			return err
		}
		value.Msg.Item, err = rpcCurrentItem(ctx, cfg, id)
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "close":
		value, err := cfg.API.Lifecycle.CloseItem(ctx, rpcRequest(cfg, &v1.CloseItemRequest{ItemId: id, Resolution: v1.Resolution(v1.Resolution_value["RESOLUTION_"+strings.ToUpper(*resolution)]), Force: *force, CommitChanges: *commit, ConfirmAttachedTerminal: *confirmed}))
		if err != nil {
			return err
		}
		value.Msg.Job, err = rpcWaitJob(ctx, cfg, value.Msg.Job, jsonOut)
		if err != nil {
			return err
		}
		value.Msg.Item, err = rpcCurrentItem(ctx, cfg, id)
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "reopen":
		before, e := rpcCurrentItem(ctx, cfg, id)
		if e != nil {
			return e
		}
		if before.State != v1.ItemState_ITEM_STATE_CLOSED {
			return errors.New("reopen requires a closed item; use start for backlog work")
		}
		value, err := cfg.API.Lifecycle.ReopenItem(ctx, rpcRequest(cfg, &v1.ReopenItemRequest{ItemId: id, Start: false, Mode: wanted}))
		if err != nil {
			return err
		}
		if !*backlog {
			if settings := agentSettingFlags(*modelName, *reasoning); len(settings) > 0 {
				if _, err := cfg.API.Agents.UpdateAgentSettings(ctx, rpcRequest(cfg, &v1.UpdateAgentSettingsRequest{ItemId: id, Settings: settings})); err != nil {
					return fmt.Errorf("reopened item, but settings failed: %w", err)
				}
			}
			started, e := cfg.API.Lifecycle.StartItem(ctx, rpcRequest(cfg, &v1.StartItemRequest{ItemId: id, Start: &v1.StartOptions{Mode: wanted, SendNothing: true, SkipSetup: *skip, OverrideCapacity: *force}}))
			if e != nil {
				return fmt.Errorf("reopened item, but starting failed: %w", e)
			}
			value.Msg.Job = started.Msg.Job
		}
		value.Msg.Job, err = rpcWaitJob(ctx, cfg, value.Msg.Job, jsonOut)
		if err != nil {
			return err
		}
		value.Msg.Item, err = rpcCurrentItem(ctx, cfg, id)
		if err != nil {
			return err
		}
		if err := rpcEmit(cfg, jsonOut, value.Msg); err != nil {
			return err
		}
		if !jsonOut && !*backlog && !*noAttach && wanted == v1.AgentMode_AGENT_MODE_TUI && interactiveCLI(cfg) {
			return rpcEnterTerminal(ctx, cfg, id, false, false)
		}
		return nil
	}
	return nil
}
func rpcAgent(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{errors.New("agent requires status, runtime, control, monitor, settings or profile")}
	}
	switch args[0] {
	case "profile":
		return rpcProfiles(ctx, args[1:], cfg, jsonOut)
	case "status":
		id, err := rpcItemArgs(ctx, cfg, "agent status", args[1:])
		if err != nil {
			return err
		}
		value, err := cfg.API.Agents.GetAgent(ctx, rpcRequest(cfg, &v1.GetAgentRequest{ItemId: id}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "monitor":
		fs := newFlagSet("agent monitor", cfg.Stderr)
		selected := fs.String("item", "", "item")
		limit := fs.Int("limit", 50, "maximum live events; zero follows indefinitely")
		if err := rpcParse(fs, args[1:]); err != nil {
			return err
		}
		if *limit < 0 {
			return usageErr{errors.New("--limit must be nonnegative")}
		}
		selector, err := itemSelectorFromFlag(fs, *selected)
		if err != nil {
			return usageErr{err}
		}
		id, err := rpcSelect(ctx, cfg, selector)
		if err != nil {
			return err
		}
		stream, err := cfg.API.Agents.WatchLiveContent(ctx, rpcRequest(cfg, &v1.WatchLiveContentRequest{ItemId: id}))
		if err != nil {
			return err
		}
		defer stream.Close()
		count := 0
		for stream.Receive() {
			if err := rpcEmitStream(cfg, jsonOut, stream.Msg()); err != nil {
				return err
			}
			count++
			if *limit > 0 && count >= *limit {
				return nil
			}
		}
		return stream.Err()
	case "runtime":
		return rpcRuntime(ctx, args[1:], cfg, jsonOut)
	case "settings":
		return rpcAgentSettings(ctx, args[1:], cfg, jsonOut)
	case "control":
		return rpcControl(ctx, args[1:], cfg, jsonOut)
	}
	return usageErr{fmt.Errorf("unknown agent command %q", args[0])}
}
func rpcRuntime(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{errors.New("agent runtime requires status, ensure or stop")}
	}
	fs := newFlagSet("agent runtime "+args[0], cfg.Stderr)
	selected := fs.String("item", "", "item")
	mode := fs.String("mode", "tui", "mode")
	force := fs.Bool("force", false, "force busy stop")
	confirm := fs.Bool("confirm-attached-terminal", false, "confirm terminal disconnect")
	if err := rpcParse(fs, args[1:]); err != nil {
		return err
	}
	selector, err := itemSelectorFromFlag(fs, *selected)
	if err != nil {
		return usageErr{err}
	}
	id, err := rpcSelect(ctx, cfg, selector)
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		value, err := cfg.API.Agents.GetAgent(ctx, rpcRequest(cfg, &v1.GetAgentRequest{ItemId: id}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "ensure":
		wanted, err := rpcMode(*mode)
		if err != nil {
			return err
		}
		value, err := cfg.API.Agents.StartAgent(ctx, rpcRequest(cfg, &v1.StartAgentRequest{ItemId: id, Mode: wanted}))
		if err != nil {
			return err
		}
		value.Msg.Job, err = rpcWaitJob(ctx, cfg, value.Msg.Job, jsonOut)
		if err != nil {
			return err
		}
		current, e := cfg.API.Agents.GetAgent(ctx, rpcRequest(cfg, &v1.GetAgentRequest{ItemId: id}))
		if e != nil {
			return e
		}
		value.Msg.Agent = current.Msg.Agent
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "stop":
		value, err := cfg.API.Agents.StopAgent(ctx, rpcRequest(cfg, &v1.StopAgentRequest{ItemId: id, Force: *force, ConfirmAttachedTerminal: *confirm}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "restart":
		value, err := cfg.API.Agents.RestartAgent(ctx, rpcRequest(cfg, &v1.RestartAgentRequest{ItemId: id}))
		if err != nil {
			return err
		}
		value.Msg.Job, err = rpcWaitJob(ctx, cfg, value.Msg.Job, jsonOut)
		if err != nil {
			return err
		}
		current, e := cfg.API.Agents.GetAgent(ctx, rpcRequest(cfg, &v1.GetAgentRequest{ItemId: id}))
		if e != nil {
			return e
		}
		value.Msg.Agent = current.Msg.Agent
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	return usageErr{fmt.Errorf("unknown runtime action %q", args[0])}
}
func rpcAgentSettings(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	action := "show"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action, args = args[0], args[1:]
	}
	fs := newFlagSet("agent settings "+action, cfg.Stderr)
	selected := fs.String("item", "", "item")
	modelName := fs.String("model", "", "model")
	reasoning := fs.String("reasoning", "", "reasoning")
	restart := fs.Bool("restart", false, "allow restart")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	id, err := rpcSelect(ctx, cfg, *selected)
	if err != nil {
		return err
	}
	if action == "show" || action == "get" {
		value, err := cfg.API.Agents.GetAgentSettings(ctx, rpcRequest(cfg, &v1.GetAgentSettingsRequest{ItemId: id}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	settings := agentSettingFlags(*modelName, *reasoning)
	if settings == nil {
		settings = map[string]string{}
	}
	for _, arg := range fs.Args() {
		key, value, ok := strings.Cut(arg, "=")
		if !ok {
			return usageErr{errors.New("settings use KEY=VALUE")}
		}
		settings[key] = value
	}
	current, err := cfg.API.Agents.GetAgent(ctx, rpcRequest(cfg, &v1.GetAgentRequest{ItemId: id}))
	if err != nil {
		return err
	}
	leaseID := ""
	if current.Msg.Agent.Online {
		lease, err := rpcAcquire(ctx, cfg, id, false, false)
		if err != nil {
			return err
		}
		if lease.Control != nil && lease.Control.Lease != nil {
			leaseID = lease.Control.Lease.Id
		}
	}
	value, err := cfg.API.Agents.UpdateAgentSettings(ctx, rpcRequest(cfg, &v1.UpdateAgentSettingsRequest{ItemId: id, Settings: settings, Restart: *restart, LeaseId: leaseID}))
	if err != nil {
		return err
	}
	return rpcEmit(cfg, jsonOut, value.Msg)
}
func rpcAcquire(ctx context.Context, cfg Config, id string, confirm, start bool, tokens ...string) (*v1.AcquireControlResponse, error) {
	client := cfg.Env["WI_CLIENT_ID"]
	if client == "" {
		client = "cli"
	}
	token := ""
	if len(tokens) > 0 {
		token = tokens[0]
	}
	if confirm && token == "" {
		current, err := cfg.API.Control.GetControl(ctx, rpcRequest(cfg, &v1.GetControlRequest{ItemId: id, ClientId: client}))
		if err != nil {
			return nil, err
		}
		token = current.Msg.Control.Plan.PlanToken
	}
	value, err := cfg.API.Control.AcquireControl(ctx, rpcRequest(cfg, &v1.AcquireControlRequest{ItemId: id, ClientId: client, Label: "cli", Confirm: confirm, StartAgent: start, PlanToken: token}))
	if err != nil {
		return nil, err
	}
	return value.Msg, nil
}
func rpcClientID(cfg Config) string {
	if cfg.Env["WI_CLIENT_ID"] != "" {
		return cfg.Env["WI_CLIENT_ID"]
	}
	return "cli"
}
func rpcCurrentItem(ctx context.Context, cfg Config, id string) (*v1.Item, error) {
	value, err := cfg.API.Items.GetItem(ctx, rpcRequest(cfg, &v1.GetItemRequest{ItemId: id}))
	if err != nil {
		return nil, err
	}
	return value.Msg.Item, nil
}
func rpcControl(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{errors.New("agent control requires send, abort, shutdown or a lease action")}
	}
	action := args[0]
	fs := newFlagSet("agent control "+action, cfg.Stderr)
	selected := fs.String("item", "", "item")
	fromStdin := fs.Bool("stdin", false, "read stdin")
	file := fs.String("file", "", "read file")
	actor := fs.String("actor", "", "control source")
	followUp := fs.Bool("follow-up", false, "deliver after the current turn settles")
	confirm := fs.Bool("confirm", false, "confirm takeover")
	planToken := fs.String("plan-token", "", "observed plan token")
	clientMessage := fs.String("client-message-id", "", "message idempotency key")
	var imagePaths repeatFlag
	fs.Var(&imagePaths, "image", "image path; repeatable")
	if err := rpcParse(fs, args[1:]); err != nil {
		return err
	}
	isMessage := action == "send" || action == "prompt" || action == "steer" || action == "follow-up" || action == "follow_up"
	if !isMessage && (*file != "" || *fromStdin || len(imagePaths) > 0 || *followUp || fs.NArg() > 0) {
		return usageErr{fmt.Errorf("agent control %s does not accept message content", action)}
	}
	if *followUp && action != "send" {
		return usageErr{errors.New("--follow-up applies to agent control send")}
	}
	var message string
	var images []*v1.Image
	if isMessage {
		content, err := readAgentControlImages(cfg.CWD, imagePaths)
		if err != nil {
			return usageErr{err}
		}
		for _, image := range content {
			data, err := base64.StdEncoding.DecodeString(image.Data)
			if err != nil {
				return err
			}
			images = append(images, &v1.Image{MimeType: image.MIMEType, Data: data})
		}
		if len(images) == 0 || fs.NArg() > 0 || *file != "" || *fromStdin {
			var err error
			message, err = readAgentControlMessage(cfg, fs.Args(), *file, *fromStdin)
			if err != nil {
				return usageErr{err}
			}
		}
	}
	id, err := rpcSelect(ctx, cfg, *selected)
	if err != nil {
		return err
	}
	switch action {
	case "status":
		value, err := cfg.API.Control.GetControl(ctx, rpcRequest(cfg, &v1.GetControlRequest{ItemId: id, ClientId: rpcClientID(cfg)}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "acquire", "take":
		value, err := rpcAcquire(ctx, cfg, id, *confirm, true, *planToken)
		if err != nil {
			return err
		}
		value.Job, err = rpcWaitJob(ctx, cfg, value.Job, jsonOut)
		if err != nil {
			return err
		}
		current, err := cfg.API.Control.GetControl(ctx, rpcRequest(cfg, &v1.GetControlRequest{ItemId: id, ClientId: rpcClientID(cfg)}))
		if err != nil {
			return err
		}
		value.Control = current.Msg.Control
		return rpcEmit(cfg, jsonOut, value)
	case "release":
		current, err := cfg.API.Control.GetControl(ctx, rpcRequest(cfg, &v1.GetControlRequest{ItemId: id, ClientId: rpcClientID(cfg)}))
		if err != nil {
			return err
		}
		if current.Msg.Control.Lease == nil {
			return rpcEmit(cfg, jsonOut, &v1.ReleaseControlResponse{})
		}
		value, err := cfg.API.Control.ReleaseControl(ctx, rpcRequest(cfg, &v1.ReleaseControlRequest{LeaseId: current.Msg.Control.Lease.Id}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "abort":
		lease, err := rpcAcquire(ctx, cfg, id, *confirm, false, *planToken)
		if err != nil {
			return err
		}
		if lease.Control == nil || lease.Control.Lease == nil {
			return errors.New("no control lease")
		}
		request := rpcRequest(cfg, &v1.AbortTurnRequest{ItemId: id, LeaseId: lease.Control.Lease.Id})
		request.Header().Set("Wi-Control-Actor", *actor)
		value, err := cfg.API.Agents.AbortTurn(ctx, request)
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "shutdown":
		request := rpcRequest(cfg, &v1.StopAgentRequest{ItemId: id, ConfirmAttachedTerminal: *confirm})
		request.Header().Set("Wi-Control-Actor", *actor)
		value, err := cfg.API.Agents.StopAgent(ctx, request)
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	if !isMessage {
		return usageErr{fmt.Errorf("unknown agent control action %q", action)}
	}
	delivery := v1.Delivery_DELIVERY_UNSPECIFIED
	if action != "send" {
		delivery = v1.Delivery(v1.Delivery_value["DELIVERY_"+strings.ToUpper(strings.ReplaceAll(action, "-", "_"))])
	}
	if *followUp {
		delivery = v1.Delivery_DELIVERY_FOLLOW_UP
	}
	if *clientMessage == "" {
		*clientMessage = newCommandID()
	}
	request := rpcRequest(cfg, &v1.SendMessageRequest{ItemId: id, Text: message, Images: images, Delivery: delivery, RequireOnline: true, ClientMessageId: *clientMessage, StartMode: v1.AgentMode_AGENT_MODE_HEADLESS})
	request.Header().Set("Wi-Control-Actor", *actor)
	value, err := cfg.API.Agents.SendMessage(ctx, request)
	if err != nil {
		return err
	}
	value.Msg.Job, err = rpcWaitJob(ctx, cfg, value.Msg.Job, jsonOut)
	if err != nil {
		return err
	}
	if jsonOut {
		return rpcEmit(cfg, true, value.Msg)
	}
	fmt.Fprintf(cfg.Stdout, "submitted %s control command %s to agent generation %d\n", action, *clientMessage, value.Msg.Generation)
	return nil
}
func rpcProfiles(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	action := "list"
	if len(args) > 0 {
		action, args = args[0], args[1:]
	}
	switch action {
	case "list":
		value, err := cfg.API.Profiles.ListAgentProfiles(ctx, rpcRequest(cfg, &v1.ListAgentProfilesRequest{}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "drivers":
		value, err := cfg.API.Profiles.ListDrivers(ctx, rpcRequest(cfg, &v1.ListDriversRequest{}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	fs := newFlagSet("agent profile "+action, cfg.Stderr)
	driver := fs.String("driver", "pi", "driver")
	label := fs.String("label", "", "label")
	executable := fs.String("executable", "", "agent executable")
	modelName := fs.String("model", "", "model")
	reasoning := fs.String("reasoning", "", "reasoning")
	var settings repeatFlag
	fs.Var(&settings, "set", "KEY=VALUE")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 || (fs.NArg() > 1 && action != "add" && action != "put") {
		return usageErr{errors.New("profile action requires a name")}
	}
	settings = append(settings, fs.Args()[1:]...)
	current, err := cfg.API.Profiles.ListAgentProfiles(ctx, rpcRequest(cfg, &v1.ListAgentProfilesRequest{}))
	if err != nil {
		return err
	}
	name := fs.Arg(0)
	switch action {
	case "default":
		value, err := cfg.API.Profiles.SetDefaultAgentProfile(ctx, rpcRequest(cfg, &v1.SetDefaultAgentProfileRequest{Name: name, ExpectedRevision: current.Msg.Revision}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "remove", "delete":
		value, err := cfg.API.Profiles.DeleteAgentProfile(ctx, rpcRequest(cfg, &v1.DeleteAgentProfileRequest{Name: name, ExpectedRevision: current.Msg.Revision}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "add", "put":
		values := map[string]any{}
		for key, value := range map[string]string{"executable": *executable, "model": *modelName, "reasoning": *reasoning} {
			if value != "" {
				values[key] = value
			}
		}
		for _, entry := range settings {
			key, value, ok := strings.Cut(entry, "=")
			if !ok {
				return usageErr{errors.New("profile settings use KEY=VALUE")}
			}
			var v any
			if json.Unmarshal([]byte(value), &v) != nil {
				v = value
			}
			values[key] = v
		}
		config, err := structpb.NewStruct(values)
		if err != nil {
			return err
		}
		value, err := cfg.API.Profiles.PutAgentProfile(ctx, rpcRequest(cfg, &v1.PutAgentProfileRequest{ExpectedRevision: current.Msg.Revision, Profile: &v1.AgentProfile{Name: name, Driver: *driver, Label: *label, Config: config}}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	return usageErr{fmt.Errorf("unknown profile action %q", action)}
}
