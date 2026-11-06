package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/model"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func rpcRequest[T any](cfg Config, msg *T) *connect.Request[T] {
	req := connect.NewRequest(msg)
	client := cfg.Env["WI_CLIENT_ID"]
	if client == "" {
		client = "cli"
	}
	req.Header().Set("Wi-Client-Id", client)
	req.Header().Set("Wi-Client-Kind", model.LeaseHolderCLI)
	return req
}
func rpcEmit(cfg Config, jsonOut bool, msg proto.Message) error {
	if !jsonOut {
		return renderRPC(cfg.Stdout, msg)
	}
	data, err := (protojson.MarshalOptions{UseProtoNames: true, Indent: "  "}).Marshal(msg)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cfg.Stdout, string(data))
	return err
}
func rpcEmitStream(cfg Config, jsonOut bool, msg proto.Message) error {
	if !jsonOut {
		return renderRPC(cfg.Stdout, msg)
	}
	data, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(msg)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cfg.Stdout, string(data))
	return err
}
func rpcParse(fs *flag.FlagSet, args []string) error {
	flags, pos := []string{}, []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			flags = append(flags, arg)
			name := strings.TrimLeft(strings.SplitN(arg, "=", 2)[0], "-")
			f := fs.Lookup(name)
			if f != nil && !strings.Contains(arg, "=") {
				b, ok := f.Value.(interface{ IsBoolFlag() bool })
				if !ok || !b.IsBoolFlag() {
					if i+1 < len(args) {
						i++
						flags = append(flags, args[i])
					}
				}
			}
		} else {
			pos = append(pos, arg)
		}
	}
	if err := fs.Parse(append(append(flags, "--"), pos...)); err != nil {
		return usageErr{err}
	}
	return nil
}
func rpcSelect(ctx context.Context, cfg Config, selector string) (string, error) {
	if selector == "" {
		selector = strings.TrimSpace(cfg.Env["WI_ID"])
	}
	value, err := cfg.API.Items.ResolveItem(ctx, rpcRequest(cfg, &v1.ResolveItemRequest{Selector: selector, Path: cfg.CWD}))
	if err != nil {
		return "", err
	}
	return value.Msg.ItemId, nil
}
func rpcItemArgs(ctx context.Context, cfg Config, name string, args []string) (string, error) {
	fs := newFlagSet(name, cfg.Stderr)
	selected := fs.String("item", "", "item selector")
	if err := rpcParse(fs, args); err != nil {
		return "", err
	}
	selector, err := itemSelectorFromFlag(fs, *selected)
	if err != nil {
		return "", usageErr{err}
	}
	return rpcSelect(ctx, cfg, selector)
}
func rpcRepository(ctx context.Context, cfg Config, selector string) (string, error) {
	if selector == "" {
		id, err := rpcSelect(ctx, cfg, "")
		if err != nil {
			return "", errors.New("select a registered repository with --repo NAME")
		}
		value, err := cfg.API.Items.GetItem(ctx, rpcRequest(cfg, &v1.GetItemRequest{ItemId: id}))
		if err != nil {
			return "", err
		}
		if value.Msg.Item.Repository == nil {
			return "", errors.New("item has no registered repository")
		}
		return value.Msg.Item.Repository.Id, nil
	}
	repos, err := cfg.API.Repositories.ListRepositories(ctx, rpcRequest(cfg, &v1.ListRepositoriesRequest{IncludeArchived: true}))
	if err != nil {
		return "", err
	}
	found := []string{}
	for _, r := range repos.Msg.Repositories {
		if r.Name == selector || r.Id == selector {
			return r.Id, nil
		}
		if strings.HasPrefix(r.Id, selector) {
			found = append(found, r.Id)
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	if len(found) > 1 {
		err := connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("ambiguous repository %q", selector))
		err.Meta().Set("Wi-Error-Reason", "ambiguous")
		return "", err
	}
	return "", connect.NewError(connect.CodeNotFound, fmt.Errorf("repository %q not found", selector))
}
func rpcWaitJob(ctx context.Context, cfg Config, job *v1.Job, jsonOut bool) (*v1.Job, error) {
	if job == nil {
		return nil, nil
	}
	stream, err := cfg.API.Jobs.WatchJob(ctx, rpcRequest(cfg, &v1.WatchJobRequest{JobId: job.Id}))
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	var latest *v1.Job
	for stream.Receive() {
		latest = stream.Msg().Job
		switch latest.State {
		case v1.JobState_JOB_STATE_WAITING_FOR_APPROVAL:
			if jsonOut {
				return latest, nil
			}
			fmt.Fprintf(cfg.Stderr, "%s\nPath: %s [y/N] ", latest.Approval.Question, latest.Approval.Path)
			line, err := bufio.NewReader(cfg.Stdin).ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return latest, err
			}
			decision := v1.ApprovalDecision_APPROVAL_DECISION_DENY
			if strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes") {
				decision = v1.ApprovalDecision_APPROVAL_DECISION_ALLOW
			}
			_, err = cfg.API.Jobs.AnswerApproval(ctx, rpcRequest(cfg, &v1.AnswerApprovalRequest{JobId: latest.Id, ChallengeId: latest.Approval.ChallengeId, Decision: decision}))
			if err != nil {
				return latest, err
			}
		case v1.JobState_JOB_STATE_SUCCEEDED:
			return latest, nil
		case v1.JobState_JOB_STATE_FAILED, v1.JobState_JOB_STATE_CANCELLED:
			failure := connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("job %s: %s", latest.Id, latest.Error))
			if latest.Partial != nil {
				if detail, err := connect.NewErrorDetail(latest.Partial); err == nil {
					failure.AddDetail(detail)
				}
			}
			return latest, failure
		}
	}
	if err := stream.Err(); err != nil {
		return latest, err
	}
	return latest, errors.New("job stream ended before a terminal snapshot")
}

func Run(ctx context.Context, args []string, cfg Config) int {
	if cfg.Stdout == nil {
		cfg.Stdout = io.Discard
	}
	if cfg.Stderr == nil {
		cfg.Stderr = io.Discard
	}
	if cfg.Stdin == nil {
		cfg.Stdin = strings.NewReader("")
	}
	if cfg.Env == nil {
		cfg.Env = map[string]string{}
	}
	args, jsonOut := extractGlobals(args)
	if len(args) == 0 {
		printUsage(cfg.Stdout)
		return ExitOK
	}
	if args[0] == "help" {
		if !printHelp(cfg.Stdout, strings.Join(args[1:], " ")) {
			return ExitUsage
		}
		return ExitOK
	}
	if topic, requested := requestedHelpTopic(args); requested {
		if !printHelp(cfg.Stdout, topic) {
			return ExitUsage
		}
		return ExitOK
	}
	if cfg.API == nil {
		fmt.Fprintln(cfg.Stderr, "daemon API is not configured")
		return ExitError
	}
	var err error
	switch args[0] {
	case "new":
		err = rpcNew(ctx, args[1:], cfg, jsonOut)
	case "show":
		err = rpcShow(ctx, args[1:], cfg, jsonOut)
	case "list":
		err = rpcList(ctx, args[1:], cfg, jsonOut)
	case "search":
		err = rpcSearch(ctx, args[1:], cfg, jsonOut)
	case "edit":
		err = rpcEdit(ctx, args[1:], cfg, jsonOut)
	case "events":
		id, e := rpcItemArgs(ctx, cfg, "events", args[1:])
		if e != nil {
			err = e
			break
		}
		value, e := cfg.API.Items.ListItemEvents(ctx, rpcRequest(cfg, &v1.ListItemEventsRequest{ItemId: id}))
		if e != nil {
			err = e
			break
		}
		err = rpcEmit(cfg, jsonOut, value.Msg)
	case "state":
		err = rpcState(ctx, args[1:], cfg, jsonOut)
	case "note", "notes", "attach", "attachments", "attachment":
		err = rpcNotes(ctx, args[0], args[1:], cfg, jsonOut)
	case "workspace":
		err = rpcWorkspace(ctx, args[1:], cfg, jsonOut)
	case "agent":
		err = rpcAgent(ctx, args[1:], cfg, jsonOut)
	case "start", "resume", "shelve", "close", "reopen":
		err = rpcLifecycle(ctx, args[0], args[1:], cfg, jsonOut)
	case "wait", "blocker":
		err = rpcBlockers(ctx, args[0], args[1:], cfg, jsonOut)
	case "attention":
		err = rpcAttention(ctx, args[1:], cfg, jsonOut)
	case "repo":
		err = rpcRepo(ctx, args[1:], cfg, jsonOut)
	case "merge":
		id, e := rpcItemArgs(ctx, cfg, "merge", args[1:])
		if e != nil {
			err = e
			break
		}
		value, e := cfg.API.Repositories.IntegrateItem(ctx, rpcRequest(cfg, &v1.IntegrateItemRequest{ItemId: id}))
		if e != nil {
			err = e
			break
		}
		value.Msg.Job, err = rpcWaitJob(ctx, cfg, value.Msg.Job, jsonOut)
		if err == nil {
			err = rpcEmit(cfg, jsonOut, value.Msg)
		}
	case "delete":
		fs := newFlagSet("delete", cfg.Stderr)
		yes := fs.Bool("yes", false, "confirm deletion")
		if err = rpcParse(fs, args[1:]); err != nil {
			break
		}
		for _, selector := range fs.Args() {
			id, e := rpcSelect(ctx, cfg, selector)
			if e != nil {
				err = e
				break
			}
			value, e := cfg.API.Items.DeleteItem(ctx, rpcRequest(cfg, &v1.DeleteItemRequest{ItemId: id, Confirm: *yes}))
			if e != nil {
				err = e
				break
			}
			err = rpcEmit(cfg, jsonOut, value.Msg)
		}
	case "terminal", "switch", "next", "pick":
		err = rpcNavigation(ctx, args[0], args[1:], cfg, jsonOut)
	case "info", "version", "config", "shutdown":
		err = rpcSystem(ctx, args[0], args[1:], cfg, jsonOut)
	case "plugin":
		if len(args) > 1 && args[1] == "list" {
			value, e := cfg.API.System.ListPlugins(ctx, rpcRequest(cfg, &v1.ListPluginsRequest{}))
			if e != nil {
				err = e
				break
			}
			err = rpcEmit(cfg, jsonOut, value.Msg)
		} else {
			err = runPlugin(ctx, args[1:], cfg, jsonOut)
		}
	default:
		if code, ok := runPluginCommand(ctx, args[0], args[1:], cfg, jsonOut); ok {
			return code
		}
		err = usageErr{fmt.Errorf("unknown command %q", args[0])}
	}
	if err != nil {
		fmt.Fprintln(cfg.Stderr, err)
		var rpcError *connect.Error
		if errors.As(err, &rpcError) {
			if rpcError.Meta().Get("Wi-Error-Reason") == "ambiguous" {
				return ExitAmbiguous
			}
			for _, detail := range rpcError.Details() {
				value, e := detail.Value()
				if e == nil {
					if partial, ok := value.(*v1.PartialOutcome); ok {
						fmt.Fprintf(cfg.Stderr, "Retained item %s: %s\n", partial.ItemId, strings.Join(partial.Completed, ", "))
						for _, warning := range partial.Warnings {
							fmt.Fprintln(cfg.Stderr, "Warning:", warning)
						}
					}
				}
			}
		}
		switch connect.CodeOf(err) {
		case connect.CodeNotFound:
			return ExitNotFound
		case connect.CodeInvalidArgument:
			return ExitUsage
		}
		return exitCode(err)
	}
	return ExitOK
}
func rpcNew(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("new", cfg.Stderr)
	title := fs.String("title", "", "title")
	untitled := fs.Bool("untitled", false, "placeholder title")
	description := fs.String("desc-file", "", "description file")
	repo := fs.String("repo", "", "repository")
	base := fs.String("base", "", "base revision")
	profile := fs.String("agent", "", "agent profile")
	slug := fs.String("slug", "", "slug")
	start := fs.Bool("start", false, "start item")
	force := fs.Bool("force", false, "override deep-work capacity when starting")
	prompt := fs.String("prompt", "", "first prompt")
	mode := fs.String("agent-mode", "tui", "tui or headless")
	modelName := fs.String("model", "", "model")
	reasoning := fs.String("reasoning", "", "reasoning")
	deep := fs.Bool("deep", false, "deep work")
	noDefaults := fs.Bool("no-default-labels", false, "no default labels")
	var labels repeatFlag
	fs.Var(&labels, "label", "label")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return usageErr{errors.New("new accepts flags only")}
	}
	if *untitled && *title != "" {
		return usageErr{errors.New("--untitled and --title conflict")}
	}
	if *untitled {
		*title = model.UntitledTitle
	}
	if strings.TrimSpace(*title) == "" {
		return usageErr{errors.New("new requires --title or --untitled")}
	}
	if !*noDefaults {
		if cfg.GlobalConfig != nil {
			labels = append(labels, cfg.GlobalConfig.Item.Defaults.Labels...)
		}
		labels = append(labels, strings.FieldsFunc(cfg.Env["WI_ITEM_DEFAULT_LABELS"], func(r rune) bool { return r == ',' || r == ' ' })...)
	}
	normalized, err := model.NormalizeLabels(labels)
	if err != nil {
		return usageErr{err}
	}
	repository, err := rpcRepository(ctx, cfg, *repo)
	if err != nil {
		return err
	}
	content, err := readDescriptionFileFlag(cfg, *description)
	if err != nil {
		return err
	}
	revision, err := rpcBase(ctx, cfg, repository, *base)
	if err != nil {
		return err
	}
	request := &v1.CreateItemRequest{RepositoryId: repository, Title: *title, Description: content, Labels: normalized, DeepWork: *deep, Base: revision, AgentProfile: *profile, AgentSettings: agentSettingFlags(*modelName, *reasoning), Slug: *slug, RequestId: newCommandID()}
	if *start || *prompt != "" {
		m, err := rpcMode(*mode)
		if err != nil {
			return err
		}
		value, err := cfg.API.Lifecycle.CreateAndStartItem(ctx, rpcRequest(cfg, &v1.CreateAndStartItemRequest{Item: request, Start: &v1.StartOptions{Prompt: *prompt, Mode: m, OverrideCapacity: *force, ClientMessageId: newCommandID()}}))
		if err != nil {
			return err
		}
		value.Msg.Job, err = rpcWaitJob(ctx, cfg, value.Msg.Job, jsonOut)
		if err != nil {
			return err
		}
		value.Msg.Item, err = rpcCurrentItem(ctx, cfg, value.Msg.Item.Id)
		if err != nil {
			return err
		}
		if jsonOut {
			return rpcEmit(cfg, true, value.Msg)
		}
		fmt.Fprintf(cfg.Stdout, "%s %s\n", value.Msg.Item.Id, value.Msg.Item.Title)
		return nil
	}
	value, err := cfg.API.Items.CreateItem(ctx, rpcRequest(cfg, request))
	if err != nil {
		return err
	}
	if jsonOut {
		return rpcEmit(cfg, true, value.Msg)
	}
	fmt.Fprintf(cfg.Stdout, "%s %s\n", value.Msg.Item.Id, value.Msg.Item.Title)
	return nil
}
func rpcShow(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	id, err := rpcItemArgs(ctx, cfg, "show", args)
	if err != nil {
		return err
	}
	value, err := cfg.API.Items.GetItem(ctx, rpcRequest(cfg, &v1.GetItemRequest{ItemId: id, IncludeDescription: true}))
	if err != nil {
		return err
	}
	if jsonOut {
		return rpcEmit(cfg, true, value.Msg)
	}
	v := value.Msg.Item
	if err := renderItem(cfg.Stdout, v, value.Msg.Description); err != nil {
		return err
	}
	w, err := cfg.API.Workspaces.GetWorkspace(ctx, rpcRequest(cfg, &v1.GetWorkspaceRequest{ItemId: id}))
	if err == nil && w.Msg.Checkout != nil && w.Msg.Checkout.Claim != nil {
		fmt.Fprintf(cfg.Stdout, "Checkout: %s\nAhead: %d Behind: %d\n", w.Msg.Checkout.Claim.Path, w.Msg.Git.GetAhead(), w.Msg.Git.GetBehind())
	}
	return nil
}
func rpcList(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("list", cfg.Stderr)
	selected := fs.String("state", "", "state")
	all := fs.Bool("all", false, "include closed")
	closed := fs.Bool("closed", false, "closed items only")
	ids := fs.Bool("ids", false, "show ID column")
	noAgent := fs.Bool("no-agent", false, "omit agent and worktree inspection")
	inspect := fs.Bool("inspect", false, "refresh observations")
	repo := fs.String("repo", "", "repository")
	var labels repeatFlag
	fs.Var(&labels, "label", "label filter")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return usageErr{errors.New("list accepts flags only")}
	}
	if *closed && *all {
		return usageErr{errors.New("pass either --closed or --all, not both")}
	}
	if *closed && *selected != "" && *selected != "closed" {
		return usageErr{errors.New("--closed conflicts with --state")}
	}
	r := &v1.ListItemsRequest{Order: v1.ListOrder_LIST_ORDER_ATTENTION, Inspect: !*noAgent && (!*closed || *inspect)}
	if *all || *selected == "all" {
		r.States = []v1.ItemState{1, 2, 3}
		r.ClosedLimit = ^uint32(0)
	}
	if *closed {
		r.States = []v1.ItemState{v1.ItemState_ITEM_STATE_CLOSED}
		r.ClosedLimit = ^uint32(0)
	}
	if *selected != "" && *selected != "all" {
		v, err := rpcStateValue(*selected)
		if err != nil {
			return err
		}
		r.States = []v1.ItemState{v}
		if v == v1.ItemState_ITEM_STATE_CLOSED {
			r.Inspect = false
			r.ClosedLimit = ^uint32(0)
		}
	}
	if *repo != "" {
		id, err := rpcRepository(ctx, cfg, *repo)
		if err != nil {
			return err
		}
		r.RepositoryIds = []string{id}
	}
	defaults := []string{}
	if cfg.GlobalConfig != nil {
		defaults = cfg.GlobalConfig.List.Labels
	}
	rules, err := effectiveListLabelRules(defaults, cfg.Env, labels)
	if err != nil {
		return usageErr{err}
	}
	r.LabelRules = apiLabelRules(rules)
	value, err := cfg.API.Items.ListItems(ctx, rpcRequest(cfg, r))
	if err != nil {
		return err
	}
	if jsonOut {
		return rpcEmit(cfg, true, value.Msg)
	}
	mode := "active"
	if *all || *selected == "all" {
		mode = "all"
	}
	if *closed || *selected == "closed" {
		mode = "archived"
	}
	return rpcRenderList(ctx, cfg, value.Msg, mode, *ids, *noAgent)
}
func rpcSearch(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("search", cfg.Stderr)
	state := fs.String("state", "", "state")
	limit := fs.Uint("limit", 50, "result limit")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	r := &v1.SearchItemsRequest{Query: strings.Join(fs.Args(), " "), Limit: uint32(*limit)}
	if *state != "" && *state != "all" {
		v, err := rpcStateValue(*state)
		if err != nil {
			return err
		}
		r.States = []v1.ItemState{v}
	}
	value, err := cfg.API.Items.SearchItems(ctx, rpcRequest(cfg, r))
	if err != nil {
		return err
	}
	if jsonOut {
		return rpcEmit(cfg, true, value.Msg)
	}
	for _, hit := range value.Msg.Hits {
		fmt.Fprintf(cfg.Stdout, "%s  %s  %s\n", hit.Item.Slug, hit.Item.Title, hit.Excerpt)
	}
	return nil
}
func rpcStateValue(value string) (v1.ItemState, error) {
	v := v1.ItemState(v1.ItemState_value["ITEM_STATE_"+strings.ToUpper(value)])
	if v == 0 {
		return 0, usageErr{fmt.Errorf("unknown state %q", value)}
	}
	return v, nil
}
func rpcMode(value string) (v1.AgentMode, error) {
	switch value {
	case "tui":
		return v1.AgentMode_AGENT_MODE_TUI, nil
	case "headless":
		return v1.AgentMode_AGENT_MODE_HEADLESS, nil
	}
	return 0, usageErr{fmt.Errorf("unknown agent mode %q", value)}
}
func rpcState(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{errors.New("state requires show or set")}
	}
	if args[0] == "show" {
		return rpcShow(ctx, args[1:], cfg, jsonOut)
	}
	if args[0] != "set" || len(args) < 2 {
		return usageErr{errors.New("state set STATE [ITEM]")}
	}
	fs := newFlagSet("state set", cfg.Stderr)
	selected := fs.String("item", "", "item")
	resolution := fs.String("resolution", "", "resolution")
	force := fs.Bool("force", false, "override deep-work capacity")
	if err := rpcParse(fs, args[2:]); err != nil {
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
	state, err := rpcStateValue(args[1])
	if err != nil {
		return err
	}
	value, err := cfg.API.Items.SetItemState(ctx, rpcRequest(cfg, &v1.SetItemStateRequest{ItemId: id, State: state, OverrideCapacity: *force, Resolution: v1.Resolution(v1.Resolution_value["RESOLUTION_"+strings.ToUpper(*resolution)])}))
	if err != nil {
		return err
	}
	return rpcEmit(cfg, jsonOut, value.Msg)
}
func rpcEdit(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("edit", cfg.Stderr)
	selected := fs.String("item", "", "item")
	title := fs.String("title", "", "title")
	description := fs.String("desc-file", "", "description file")
	deep := fs.Bool("deep", false, "deep work")
	var add, remove repeatFlag
	fs.Var(&add, "label", "add label")
	fs.Var(&remove, "remove-label", "remove label")
	if err := rpcParse(fs, args); err != nil {
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
	shown, err := cfg.API.Items.GetItem(ctx, rpcRequest(cfg, &v1.GetItemRequest{ItemId: id, IncludeDescription: true}))
	if err != nil {
		return err
	}
	r := &v1.UpdateItemRequest{ItemId: id, ExpectedRevision: shown.Msg.Item.Revision, AddLabels: add, RemoveLabels: remove}
	count := 0
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "item" {
			return
		}
		count++
		switch f.Name {
		case "title":
			r.Title = title
		case "deep":
			r.DeepWork = deep
		}
	})
	if *description != "" {
		content, err := readDescriptionFileFlag(cfg, *description)
		if err != nil {
			return err
		}
		r.Description = &content
	}
	if count == 0 {
		newTitle, content, _, err := editDocument(ctx, cfg, shown.Msg.Item.Title, shown.Msg.Description)
		if err != nil {
			return err
		}
		r.Title = &newTitle
		r.Description = &content
	}
	value, err := cfg.API.Items.UpdateItem(ctx, rpcRequest(cfg, r))
	if err != nil {
		return err
	}
	return rpcEmit(cfg, jsonOut, value.Msg)
}
func rpcNotes(ctx context.Context, command string, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet(command, cfg.Stderr)
	selected := fs.String("item", "", "item")
	name := fs.String("as", "", "attachment name")
	text := fs.String("m", "", "attachment note")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	selector := *selected
	if command == "notes" || command == "attachments" {
		if selector == "" {
			selector = strings.Join(fs.Args(), " ")
		}
	}
	id, err := rpcSelect(ctx, cfg, selector)
	if err != nil {
		return err
	}
	switch command {
	case "note":
		value, err := cfg.API.Items.AddNote(ctx, rpcRequest(cfg, &v1.AddNoteRequest{ItemId: id, Text: strings.Join(fs.Args(), " ")}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "attach":
		if fs.NArg() != 1 {
			return usageErr{errors.New("attach requires one file")}
		}
		path := fs.Arg(0)
		if !filepath.IsAbs(path) {
			path = filepath.Join(cfg.CWD, path)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if *name == "" {
			*name = filepath.Base(path)
		}
		r := &v1.AttachRequest{ItemId: id, Name: *name, Content: content, Text: *text}
		item, err := cfg.API.Items.GetItem(ctx, rpcRequest(cfg, &v1.GetItemRequest{ItemId: id}))
		if err != nil {
			return err
		}
		if claim := item.Msg.Item.GetCheckout().GetClaim(); claim != nil {
			relative, err := filepath.Rel(claim.Path, path)
			if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				r.CheckoutPath = filepath.ToSlash(relative)
			}
		}
		value, err := cfg.API.Items.Attach(ctx, rpcRequest(cfg, r))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "notes", "attachments":
		value, err := cfg.API.Items.ListNotes(ctx, rpcRequest(cfg, &v1.ListNotesRequest{ItemId: id}))
		if err != nil {
			return err
		}
		if command == "attachments" && !jsonOut {
			if len(value.Msg.Attachments) == 0 {
				fmt.Fprintln(cfg.Stdout, "no attachments")
			}
			for _, current := range value.Msg.Attachments {
				a := current.Attachment
				fmt.Fprintf(cfg.Stdout, "%s  %d bytes  %s\n", a.Name, a.Size, a.Sha256)
			}
			return nil
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "attachment":
		pos := fs.Args()
		if len(pos) > 0 && pos[0] == "cat" {
			pos = pos[1:]
		}
		if len(pos) != 1 {
			return usageErr{errors.New("attachment cat NAME")}
		}
		notes, err := cfg.API.Items.ListNotes(ctx, rpcRequest(cfg, &v1.ListNotesRequest{ItemId: id}))
		if err != nil {
			return err
		}
		digest := ""
		for _, a := range notes.Msg.Attachments {
			if a.Attachment.Name == pos[0] || a.Attachment.Sha256 == pos[0] {
				digest = a.Attachment.Sha256
			}
		}
		if digest == "" {
			return connect.NewError(connect.CodeNotFound, errors.New("attachment not found"))
		}
		stream, err := cfg.API.Items.ReadAttachment(ctx, rpcRequest(cfg, &v1.ReadAttachmentRequest{ItemId: id, Sha256: digest}))
		if err != nil {
			return err
		}
		defer stream.Close()
		for stream.Receive() {
			if jsonOut {
				if err := rpcEmit(cfg, true, stream.Msg()); err != nil {
					return err
				}
			} else {
				if _, err := cfg.Stdout.Write(stream.Msg().Chunk); err != nil {
					return err
				}
			}
		}
		return stream.Err()
	}
	return nil
}
