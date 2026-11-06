package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func rpcRepo(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{errors.New("repo requires a command")}
	}
	action := args[0]
	switch action {
	case "settings":
		return rpcRepoSettings(ctx, args[1:], cfg, jsonOut)
	case "files":
		return rpcRepoFiles(ctx, args[1:], cfg, jsonOut)
	case "env":
		return rpcRepoEnv(ctx, args[1:], cfg, jsonOut)
	case "remote", "upstream":
		return rpcRepoUpstream(ctx, args[1:], cfg, jsonOut)
	}
	fs := newFlagSet("repo "+action, cfg.Stderr)
	selected := fs.String("repo", "", "repository")
	all := fs.Bool("all", false, "include archived")
	source := fs.String("from", "", "source")
	branch := fs.String("branch", "", "integration branch")
	upstream := fs.String("upstream", "", "upstream URL")
	upstreamBranch := fs.String("upstream-branch", "", "upstream branch")
	local := fs.Bool("local", false, "no upstream")
	fetch := fs.Bool("fetch", false, "fetch upstream")
	merge := fs.Bool("merge", false, "merge divergence")
	continuing := fs.Bool("continue", false, "continue merge")
	abort := fs.Bool("abort", false, "abort merge")
	reset := fs.Bool("reset-to-upstream", false, "reset integration branch")
	confirm := fs.Bool("yes", false, "confirm reset")
	if err := rpcParse(fs, args[1:]); err != nil {
		return err
	}
	if action == "list" {
		value, err := cfg.API.Repositories.ListRepositories(ctx, rpcRequest(cfg, &v1.ListRepositoriesRequest{IncludeArchived: *all}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	if action == "add" {
		if fs.NArg() != 1 || *source == "" {
			return usageErr{errors.New("repo add NAME --from SOURCE")}
		}
		if !filepath.IsAbs(*source) && !strings.Contains(*source, ":") {
			*source = filepath.Join(cfg.CWD, *source)
		}
		value, err := cfg.API.Repositories.AddRepository(ctx, rpcRequest(cfg, &v1.AddRepositoryRequest{Name: fs.Arg(0), Source: *source, Branch: *branch, Local: *local, UpstreamUrl: *upstream, UpstreamBranch: *upstreamBranch}))
		if err != nil {
			return err
		}
		value.Msg.Job, err = rpcWaitJob(ctx, cfg, value.Msg.Job, jsonOut)
		if err != nil {
			return err
		}
		if value.Msg.Job != nil && value.Msg.Job.RepositoryId != "" {
			repo, err := cfg.API.Repositories.GetRepository(ctx, rpcRequest(cfg, &v1.GetRepositoryRequest{RepositoryId: value.Msg.Job.RepositoryId}))
			if err != nil {
				return err
			}
			value.Msg.Repository = repo.Msg.Repository
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	selector := *selected
	if selector == "" && fs.NArg() > 0 {
		selector = fs.Arg(0)
	}
	repo, err := rpcRepository(ctx, cfg, selector)
	if err != nil {
		return err
	}
	switch action {
	case "show":
		value, err := cfg.API.Repositories.GetRepository(ctx, rpcRequest(cfg, &v1.GetRepositoryRequest{RepositoryId: repo}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "rename":
		if fs.NArg() != 2 {
			return usageErr{errors.New("repo rename OLD NEW")}
		}
		value, err := cfg.API.Repositories.RenameRepository(ctx, rpcRequest(cfg, &v1.RenameRepositoryRequest{RepositoryId: repo, Name: fs.Arg(1)}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "archive":
		value, err := cfg.API.Repositories.ArchiveRepository(ctx, rpcRequest(cfg, &v1.ArchiveRepositoryRequest{RepositoryId: repo}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "restore":
		value, err := cfg.API.Repositories.RestoreRepository(ctx, rpcRequest(cfg, &v1.RestoreRepositoryRequest{RepositoryId: repo}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "status":
		value, err := cfg.API.Repositories.GetRepositoryStatus(ctx, rpcRequest(cfg, &v1.GetRepositoryStatusRequest{RepositoryId: repo, Fetch: *fetch}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "sync":
		mode := v1.SyncMode_SYNC_MODE_FAST_FORWARD
		if *merge {
			mode = v1.SyncMode_SYNC_MODE_MERGE
		}
		if *continuing {
			mode = v1.SyncMode_SYNC_MODE_CONTINUE
		}
		if *abort {
			mode = v1.SyncMode_SYNC_MODE_ABORT
		}
		if *reset {
			mode = v1.SyncMode_SYNC_MODE_RESET_TO_UPSTREAM
		}
		value, err := cfg.API.Repositories.SyncRepository(ctx, rpcRequest(cfg, &v1.SyncRepositoryRequest{RepositoryId: repo, Mode: mode, Confirm: *confirm}))
		if err != nil {
			return err
		}
		value.Msg.Job, err = rpcWaitJob(ctx, cfg, value.Msg.Job, jsonOut)
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	return usageErr{fmt.Errorf("unknown repository action %q", action)}
}
func rpcRepoUpstream(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{errors.New("repo upstream requires show, set or clear")}
	}
	fs := newFlagSet("repo upstream "+args[0], cfg.Stderr)
	selected := fs.String("repo", "", "repository")
	branch := fs.String("branch", "", "upstream branch")
	if err := rpcParse(fs, args[1:]); err != nil {
		return err
	}
	repo, err := rpcRepository(ctx, cfg, *selected)
	if err != nil {
		return err
	}
	if args[0] == "show" {
		value, err := cfg.API.Repositories.GetRepository(ctx, rpcRequest(cfg, &v1.GetRepositoryRequest{RepositoryId: repo}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	url := ""
	if args[0] != "clear" && args[0] != "unset" {
		if fs.NArg() != 1 {
			return usageErr{errors.New("repo upstream set URL --branch BRANCH")}
		}
		url = fs.Arg(0)
	}
	value, err := cfg.API.Repositories.SetUpstream(ctx, rpcRequest(cfg, &v1.SetUpstreamRequest{RepositoryId: repo, Url: url, Branch: *branch}))
	if err != nil {
		return err
	}
	return rpcEmit(cfg, jsonOut, value.Msg)
}
func rpcSetting(value any) *v1.SettingValue {
	switch v := value.(type) {
	case string:
		return &v1.SettingValue{Value: &v1.SettingValue_StringValue{StringValue: v}}
	case bool:
		return &v1.SettingValue{Value: &v1.SettingValue_BoolValue{BoolValue: v}}
	case float64:
		return &v1.SettingValue{Value: &v1.SettingValue_IntValue{IntValue: int32(v)}}
	case []any:
		list := []string{}
		for _, entry := range v {
			list = append(list, fmt.Sprint(entry))
		}
		return &v1.SettingValue{Value: &v1.SettingValue_ListValue{ListValue: &v1.StringList{Values: list}}}
	}
	return nil
}
func rpcRepoSettings(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	action := "show"
	if len(args) > 0 {
		action, args = args[0], args[1:]
	}
	fs := newFlagSet("repo settings "+action, cfg.Stderr)
	selected := fs.String("repo", "", "repository")
	sensitive := fs.Bool("show-sensitive", false, "include secrets")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	pos := fs.Args()
	selector := *selected
	if selector == "" && len(pos) > 0 && !strings.HasPrefix(pos[0], "plugins.") {
		selector = pos[0]
		pos = pos[1:]
	}
	repo, err := rpcRepository(ctx, cfg, selector)
	if err != nil {
		return err
	}
	if action == "show" {
		value, err := cfg.API.Repositories.GetRepositorySettings(ctx, rpcRequest(cfg, &v1.GetRepositorySettingsRequest{RepositoryId: repo, ShowSensitive: *sensitive}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	if len(pos) < 1 {
		return usageErr{errors.New("repository setting key required")}
	}
	current, err := cfg.API.Repositories.GetRepositorySettings(ctx, rpcRequest(cfg, &v1.GetRepositorySettingsRequest{RepositoryId: repo}))
	if err != nil {
		return err
	}
	key := pos[0]
	if action == "unset" {
		value, err := cfg.API.Repositories.UnsetRepositorySetting(ctx, rpcRequest(cfg, &v1.UnsetRepositorySettingRequest{RepositoryId: repo, Key: key, ExpectedRevision: current.Msg.Revision}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	if len(pos) != 2 {
		return usageErr{errors.New("repo settings set [REPO] KEY VALUE")}
	}
	var typed any
	if json.Unmarshal([]byte(pos[1]), &typed) != nil {
		typed = pos[1]
	}
	value, err := cfg.API.Repositories.SetRepositorySetting(ctx, rpcRequest(cfg, &v1.SetRepositorySettingRequest{RepositoryId: repo, Key: key, Value: rpcSetting(typed), ExpectedRevision: current.Msg.Revision}))
	if err != nil {
		return err
	}
	return rpcEmit(cfg, jsonOut, value.Msg)
}
func rpcRepoFiles(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	action := "list"
	if len(args) > 0 {
		action, args = args[0], args[1:]
	}
	fs := newFlagSet("repo files "+action, cfg.Stderr)
	selected := fs.String("repo", "", "repository")
	as := fs.String("as", "", "checkout path")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	repo, err := rpcRepository(ctx, cfg, *selected)
	if err != nil {
		return err
	}
	if action == "list" {
		value, err := cfg.API.Repositories.ListRepositoryFiles(ctx, rpcRequest(cfg, &v1.ListRepositoryFilesRequest{RepositoryId: repo}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	if fs.NArg() != 1 {
		return usageErr{errors.New("repository file path required")}
	}
	path := fs.Arg(0)
	switch action {
	case "add", "put":
		source := path
		if !filepath.IsAbs(source) {
			source = filepath.Join(cfg.CWD, source)
		}
		content, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if *as != "" {
			path = *as
		} else {
			path = filepath.Base(path)
		}
		value, err := cfg.API.Repositories.PutRepositoryFile(ctx, rpcRequest(cfg, &v1.PutRepositoryFileRequest{RepositoryId: repo, Path: path, Content: content}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "remove", "delete":
		value, err := cfg.API.Repositories.DeleteRepositoryFile(ctx, rpcRequest(cfg, &v1.DeleteRepositoryFileRequest{RepositoryId: repo, Path: path}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "cat", "read":
		value, err := cfg.API.Repositories.ReadRepositoryFile(ctx, rpcRequest(cfg, &v1.ReadRepositoryFileRequest{RepositoryId: repo, Path: path}))
		if err != nil {
			return err
		}
		if jsonOut {
			return rpcEmit(cfg, true, value.Msg)
		}
		_, err = cfg.Stdout.Write(value.Msg.Content)
		return err
	}
	return usageErr{fmt.Errorf("unknown file action %q", action)}
}
func rpcRepoEnv(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	action := "show"
	if len(args) > 0 {
		action, args = args[0], args[1:]
	}
	fs := newFlagSet("repo env "+action, cfg.Stderr)
	selected := fs.String("repo", "", "repository")
	showValues := fs.Bool("show-values", false, "show environment values")
	fs.BoolVar(showValues, "show-sensitive", false, "show environment values")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	repo, err := rpcRepository(ctx, cfg, *selected)
	if err != nil {
		return err
	}
	current, err := cfg.API.Repositories.GetEnvironment(ctx, rpcRequest(cfg, &v1.GetEnvironmentRequest{RepositoryId: repo}))
	if err != nil {
		return err
	}
	if action == "show" || action == "list" {
		if !*showValues {
			for _, variable := range current.Msg.Variables {
				variable.Value = "[redacted]"
			}
		}
		return rpcEmit(cfg, jsonOut, current.Msg)
	}
	r := &v1.UpdateEnvironmentRequest{RepositoryId: repo, ExpectedRevision: current.Msg.Revision}
	if action == "unset" {
		r.Unset = fs.Args()
	} else {
		for _, arg := range fs.Args() {
			key, value, ok := strings.Cut(arg, "=")
			if !ok {
				return usageErr{errors.New("variables use KEY=VALUE")}
			}
			r.Set = append(r.Set, &v1.EnvironmentVariable{Key: key, Value: value})
		}
	}
	value, err := cfg.API.Repositories.UpdateEnvironment(ctx, rpcRequest(cfg, r))
	if err != nil {
		return err
	}
	return rpcEmit(cfg, jsonOut, value.Msg)
}
