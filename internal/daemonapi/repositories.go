package daemonapi

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitransport"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	setupplugin "github.com/regb/workitem/internal/plugins/setup"
	"github.com/regb/workitem/internal/repofiles"
)

func repository(value model.OwnedRepository) *v1.Repository {
	out := &v1.Repository{Id: value.ID, Name: value.Name, IntegrationBranch: value.IntegrationBranch, Archived: value.Archived, CreatedAt: stamp(value.CreatedAt)}
	return out
}
func (s *Server) repo(ctx context.Context, repositoryID string) (*v1.Repository, error) {
	source, err := s.Local.RepositorySource(ctx, repositoryID, "")
	if err != nil {
		return nil, err
	}
	out := repository(source.Repository.OwnedRepository)
	if source.Repository.Upstream != nil {
		out.Upstream = &v1.Upstream{Url: source.Repository.Upstream.URL, Branch: source.Repository.Upstream.Branch}
	}
	out.IntegrationCommit = source.Commit
	return out, nil
}
func (s *Server) ListRepositories(ctx context.Context, req *connect.Request[v1.ListRepositoriesRequest]) (*connect.Response[v1.ListRepositoriesResponse], error) {
	if apitransport.Caller(ctx) == apitransport.Agent {
		values, err := s.Local.ListRepositories(ctx)
		out := &v1.ListRepositoriesResponse{Repositories: []*v1.Repository{}}
		for _, repo := range values {
			if !repo.Archived || req.Msg.IncludeArchived {
				out.Repositories = append(out.Repositories, repository(repo))
			}
		}
		return response(out, err)
	}
	values, err := s.Local.ListRepositoryViews(ctx)
	out := &v1.ListRepositoriesResponse{Repositories: []*v1.Repository{}}
	for _, value := range values {
		if value.Archived && !req.Msg.IncludeArchived {
			continue
		}
		r := repository(value.OwnedRepository)
		r.Upstream = nil
		if value.Upstream != nil {
			r.Upstream = &v1.Upstream{Url: value.Upstream.URL, Branch: value.Upstream.Branch}
		}
		out.Repositories = append(out.Repositories, r)
	}
	return response(out, err)
}
func (s *Server) GetRepository(ctx context.Context, req *connect.Request[v1.GetRepositoryRequest]) (*connect.Response[v1.GetRepositoryResponse], error) {
	value, err := s.repo(ctx, req.Msg.RepositoryId)
	return response(&v1.GetRepositoryResponse{Repository: value}, err)
}
func (s *Server) AddRepository(ctx context.Context, req *connect.Request[v1.AddRepositoryRequest]) (*connect.Response[v1.AddRepositoryResponse], error) {
	r := req.Msg
	if !model.ValidRepositoryName(r.Name) {
		return nil, invalid("invalid repository name")
	}
	if r.Source == "" || (!filepath.IsAbs(r.Source) && !strings.Contains(r.Source, ":")) {
		return nil, invalid("source must be a Git URL or absolute path")
	}
	job := s.startJobKey("add_repository", "add_repository:"+r.Name, "", "", func(ctx context.Context, j *jobRun) error {
		unlock := s.lock(":repositories")
		defer unlock()
		added, err := s.onboardRepository(ctx, repositoryOnboarding{Name: r.Name, Source: r.Source, Branch: r.Branch, Local: r.Local, Upstream: r.UpstreamUrl, UpstreamBranch: r.UpstreamBranch})
		if err == nil {
			j.change(func(value *v1.Job) { value.RepositoryId = added.Record.ID })
		}
		return err
	})
	return response(&v1.AddRepositoryResponse{Job: job}, nil)
}
func (s *Server) RenameRepository(ctx context.Context, req *connect.Request[v1.RenameRepositoryRequest]) (*connect.Response[v1.RenameRepositoryResponse], error) {
	source, err := s.Local.RepositorySource(ctx, req.Msg.RepositoryId, "")
	if err != nil {
		return nil, rpcError(err)
	}
	value, err := s.Local.RenameRepository(ctx, coordinator.RenameRepositoryCommand{ID: id(), ProtocolVersion: coordinator.ProtocolVersion, RepositoryID: source.Repository.ID, PreviousName: source.Repository.Name, Name: req.Msg.Name, CreatedAt: time.Now().UTC()})
	return response(&v1.RenameRepositoryResponse{Repository: repository(value.Repository)}, err)
}
func (s *Server) archiveRepository(ctx context.Context, repositoryID string, archive bool) (*v1.Repository, error) {
	source, err := s.Local.RepositorySource(ctx, repositoryID, "")
	if err != nil {
		return nil, err
	}
	value, err := s.Local.SetRepositoryArchived(ctx, coordinator.SetRepositoryArchivedCommand{ID: id(), ProtocolVersion: coordinator.ProtocolVersion, RepositoryID: repositoryID, PreviousArchived: source.Repository.Archived, Archived: archive, CreatedAt: time.Now().UTC()})
	return repository(value.Repository), err
}
func (s *Server) ArchiveRepository(ctx context.Context, req *connect.Request[v1.ArchiveRepositoryRequest]) (*connect.Response[v1.ArchiveRepositoryResponse], error) {
	value, err := s.archiveRepository(ctx, req.Msg.RepositoryId, true)
	return response(&v1.ArchiveRepositoryResponse{Repository: value}, err)
}
func (s *Server) RestoreRepository(ctx context.Context, req *connect.Request[v1.RestoreRepositoryRequest]) (*connect.Response[v1.RestoreRepositoryResponse], error) {
	value, err := s.archiveRepository(ctx, req.Msg.RepositoryId, false)
	return response(&v1.RestoreRepositoryResponse{Repository: value}, err)
}
func (s *Server) SetUpstream(ctx context.Context, req *connect.Request[v1.SetUpstreamRequest]) (*connect.Response[v1.SetUpstreamResponse], error) {
	source, err := s.Local.RepositorySource(ctx, req.Msg.RepositoryId, "")
	if err != nil {
		return nil, rpcError(err)
	}
	var upstream *model.RepositoryUpstream
	if req.Msg.Url != "" {
		upstream, err = model.ParseRepositoryUpstream(req.Msg.Url, req.Msg.Branch)
		if err != nil {
			return nil, invalid(err.Error())
		}
	}
	_, err = s.Local.SetRepositoryUpstream(ctx, coordinator.SetRepositoryUpstreamCommand{ID: id(), ProtocolVersion: coordinator.ProtocolVersion, RepositoryID: req.Msg.RepositoryId, PreviousUpstream: source.Repository.Upstream, Upstream: upstream, CreatedAt: time.Now().UTC()})
	if err != nil {
		return nil, rpcError(err)
	}
	value, err := s.repo(ctx, req.Msg.RepositoryId)
	return response(&v1.SetUpstreamResponse{Repository: value}, err)
}
func (s *Server) GetRepositoryStatus(ctx context.Context, req *connect.Request[v1.GetRepositoryStatusRequest]) (*connect.Response[v1.GetRepositoryStatusResponse], error) {
	value, err := s.Local.RepositoryStatus(ctx, coordinator.RepositoryStatusRequest{Selector: req.Msg.RepositoryId, Fetch: req.Msg.Fetch})
	return response(&v1.GetRepositoryStatusResponse{IntegrationCommit: value.LocalSHA, UpstreamCommit: value.UpstreamSHA, Ahead: uint32(value.Ahead), Behind: uint32(value.Behind), Diverged: value.Ahead > 0 && value.Behind > 0, Reconciling: value.Pending != nil, FetchedAt: optionalStamp(value.FetchedAt)}, err)
}
func (s *Server) SyncRepository(ctx context.Context, req *connect.Request[v1.SyncRepositoryRequest]) (*connect.Response[v1.SyncRepositoryResponse], error) {
	r := req.Msg
	syncMode := "ff"
	switch r.Mode {
	case v1.SyncMode_SYNC_MODE_UNSPECIFIED, v1.SyncMode_SYNC_MODE_FAST_FORWARD:
	case v1.SyncMode_SYNC_MODE_MERGE:
		syncMode = "merge"
	case v1.SyncMode_SYNC_MODE_CONTINUE:
		syncMode = "continue"
	case v1.SyncMode_SYNC_MODE_ABORT:
		syncMode = "abort"
	case v1.SyncMode_SYNC_MODE_RESET_TO_UPSTREAM:
		if !r.Confirm {
			return nil, precondition("reset requires confirm")
		}
		syncMode = "reset"
	default:
		return nil, invalid("unknown sync mode")
	}
	if _, err := s.repo(ctx, r.RepositoryId); err != nil {
		return nil, rpcError(err)
	}
	job := s.startJobKey("sync_repository", "sync_repository:"+syncMode, "", r.RepositoryId, func(ctx context.Context, j *jobRun) error {
		request := coordinator.RepositorySyncRequest{Selector: r.RepositoryId, Mode: syncMode, ConfirmReset: r.Confirm}
		if syncMode == "reset" {
			status, err := s.Local.RepositoryStatus(ctx, coordinator.RepositoryStatusRequest{Selector: r.RepositoryId, Fetch: true})
			if err != nil {
				return err
			}
			request.ExpectedLocalSHA, request.ExpectedUpstreamSHA = status.LocalSHA, status.UpstreamSHA
		}
		_, err := s.Local.SyncRepository(ctx, request)
		return err
	})
	return response(&v1.SyncRepositoryResponse{Job: job}, nil)
}
func (s *Server) IntegrateItem(ctx context.Context, req *connect.Request[v1.IntegrateItemRequest]) (*connect.Response[v1.IntegrateItemResponse], error) {
	if _, err := s.canonical(ctx, req.Msg.ItemId, 0); err != nil {
		return nil, rpcError(err)
	}
	job := s.startJobKey("integrate_item", "integrate_item:"+req.Msg.TargetBranch+fmt.Sprint(req.Msg.Local), req.Msg.ItemId, "", func(ctx context.Context, j *jobRun) error {
		unlock := s.lock(req.Msg.ItemId)
		defer unlock()
		result, err := s.App.MergeWorkItem(ctx, app.MergeOptions{Selector: req.Msg.ItemId, Target: req.Msg.TargetBranch, Local: req.Msg.Local})
		if err != nil && (result.TargetAdvanced || result.SourcePreserved) {
			return partial(err, req.Msg.ItemId, result.Warnings...)
		}
		return err
	})
	return response(&v1.IntegrateItemResponse{Job: job}, nil)
}
func setting(value any) *v1.SettingValue {
	switch value := value.(type) {
	case string:
		return &v1.SettingValue{Value: &v1.SettingValue_StringValue{StringValue: value}}
	case bool:
		return &v1.SettingValue{Value: &v1.SettingValue_BoolValue{BoolValue: value}}
	case int:
		return &v1.SettingValue{Value: &v1.SettingValue_IntValue{IntValue: int32(value)}}
	case int64:
		return &v1.SettingValue{Value: &v1.SettingValue_IntValue{IntValue: int32(value)}}
	case float64:
		return &v1.SettingValue{Value: &v1.SettingValue_IntValue{IntValue: int32(value)}}
	case []string:
		return &v1.SettingValue{Value: &v1.SettingValue_ListValue{ListValue: &v1.StringList{Values: value}}}
	case []any:
		out := []string{}
		for _, v := range value {
			out = append(out, fmt.Sprint(v))
		}
		return setting(out)
	}
	return nil
}
func settingAny(value *v1.SettingValue) any {
	if value == nil {
		return nil
	}
	switch v := value.Value.(type) {
	case *v1.SettingValue_StringValue:
		return v.StringValue
	case *v1.SettingValue_BoolValue:
		return v.BoolValue
	case *v1.SettingValue_IntValue:
		return int64(v.IntValue)
	case *v1.SettingValue_ListValue:
		if v.ListValue != nil {
			return v.ListValue.Values
		}
	}
	return nil
}
func (s *Server) GetRepositorySettings(ctx context.Context, req *connect.Request[v1.GetRepositorySettingsRequest]) (*connect.Response[v1.GetRepositorySettingsResponse], error) {
	current, err := s.Local.RepositorySettings(ctx, req.Msg.RepositoryId)
	if err != nil {
		return nil, rpcError(err)
	}
	effective, warnings, err := s.Plugins.Registry.Options().ResolveRepository(s.Config.PluginSettings, current.Settings.Namespaces)
	if err != nil {
		return nil, rpcError(err)
	}
	out := &v1.GetRepositorySettingsResponse{Revision: uint32(current.Settings.Revision), Warnings: warnings, Settings: []*v1.RepositorySetting{}}
	for _, def := range s.Plugins.Registry.Options().Definitions() {
		for _, opt := range def.Options {
			if !opt.Repository {
				continue
			}
			value := effective[def.Name][opt.Name]
			origin := "default"
			if _, ok := s.Config.PluginSettings[def.Name][opt.Name]; ok {
				origin = "global"
			}
			if _, ok := current.Settings.Namespaces["plugins."+def.Name][opt.Name]; ok {
				origin = "repository"
			}
			row := &v1.RepositorySetting{Key: "plugins." + def.Name + "." + opt.Name, Origin: origin, Sensitive: opt.Secret}
			if !opt.Secret || req.Msg.ShowSensitive {
				row.Effective = setting(value)
			}
			out.Settings = append(out.Settings, row)
		}
	}
	sort.Slice(out.Settings, func(i, j int) bool { return out.Settings[i].Key < out.Settings[j].Key })
	return response(out, nil)
}
func (s *Server) setRepositorySetting(ctx context.Context, repositoryID, key string, value *v1.SettingValue, unset bool, expected uint32) (uint32, error) {
	parts := strings.Split(key, ".")
	if len(parts) != 3 || parts[0] != "plugins" {
		return 0, invalid("key must be plugins.PLUGIN.OPTION")
	}
	current, err := s.Local.RepositorySettings(ctx, repositoryID)
	if err != nil {
		return 0, err
	}
	if uint64(expected) != current.Settings.Revision {
		return 0, aborted("repository settings revision changed")
	}
	var raw json.RawMessage
	if !unset {
		raw, err = json.Marshal(settingAny(value))
		if err != nil {
			return 0, err
		}
	}
	if current.Settings.Revision >= uint64(^uint32(0)) {
		return 0, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("repository settings revision exhausted"))
	}
	result, err := s.Local.SetRepositorySetting(ctx, coordinator.SetRepositorySettingCommand{ID: id(), ProtocolVersion: coordinator.ProtocolVersion, RepositoryID: repositoryID, ExpectedRevision: current.Settings.Revision, Namespace: parts[0] + "." + parts[1], Key: parts[2], Value: raw, Unset: unset, CreatedAt: time.Now().UTC()})
	return uint32(result.Revision), err
}
func (s *Server) SetRepositorySetting(ctx context.Context, req *connect.Request[v1.SetRepositorySettingRequest]) (*connect.Response[v1.SetRepositorySettingResponse], error) {
	if req.Msg.Value == nil {
		return nil, invalid("value is required")
	}
	revision, err := s.setRepositorySetting(ctx, req.Msg.RepositoryId, req.Msg.Key, req.Msg.Value, false, req.Msg.ExpectedRevision)
	return response(&v1.SetRepositorySettingResponse{Revision: revision}, err)
}
func (s *Server) UnsetRepositorySetting(ctx context.Context, req *connect.Request[v1.UnsetRepositorySettingRequest]) (*connect.Response[v1.UnsetRepositorySettingResponse], error) {
	revision, err := s.setRepositorySetting(ctx, req.Msg.RepositoryId, req.Msg.Key, nil, true, req.Msg.ExpectedRevision)
	return response(&v1.UnsetRepositorySettingResponse{Revision: revision}, err)
}
func (s *Server) GetCheckoutSetup(ctx context.Context, req *connect.Request[v1.GetCheckoutSetupRequest]) (*connect.Response[v1.GetCheckoutSetupResponse], error) {
	settings, err := s.GetRepositorySettings(ctx, connect.NewRequest(&v1.GetRepositorySettingsRequest{RepositoryId: req.Msg.RepositoryId, ShowSensitive: true}))
	if err != nil {
		return nil, err
	}
	setup := &v1.CheckoutSetup{Skip: []string{}}
	for _, row := range settings.Msg.Settings {
		switch row.Key {
		case "plugins.setup.run":
			setup.Run = row.Effective.GetStringValue()
		case "plugins.setup.trust":
			if row.Effective.GetStringValue() == "always" {
				setup.Trust = v1.SetupTrust_SETUP_TRUST_ALWAYS
			} else {
				setup.Trust = v1.SetupTrust_SETUP_TRUST_NEVER
			}
		case "plugins.setup.timeout_seconds":
			setup.TimeoutSeconds = uint32(row.Effective.GetIntValue())
		case "plugins.files.skip":
			if row.Effective.GetListValue() != nil {
				setup.Skip = row.Effective.GetListValue().Values
			}
		}
	}
	source, err := s.Local.RepositorySource(ctx, req.Msg.RepositoryId, "")
	if err != nil {
		return nil, rpcError(err)
	}
	setup.Declared, _ = setupplugin.DeclaredCommand(ctx, source.GitDir, source.Repository.IntegrationBranch)
	return response(&v1.GetCheckoutSetupResponse{Setup: setup, Revision: settings.Msg.Revision}, nil)
}
func (s *Server) UpdateCheckoutSetup(ctx context.Context, req *connect.Request[v1.UpdateCheckoutSetupRequest]) (*connect.Response[v1.UpdateCheckoutSetupResponse], error) {
	if req.Msg.Setup == nil {
		return nil, invalid("setup is required")
	}
	current, err := s.Local.RepositorySettings(ctx, req.Msg.RepositoryId)
	if err != nil {
		return nil, rpcError(err)
	}
	if uint64(req.Msg.ExpectedRevision) != current.Settings.Revision {
		return nil, aborted("repository settings revision changed")
	}
	trust := "never"
	if req.Msg.Setup.Trust == v1.SetupTrust_SETUP_TRUST_ALWAYS {
		trust = "always"
	}
	changes := map[string]any{"plugins.setup.run": req.Msg.Setup.Run, "plugins.setup.trust": trust, "plugins.setup.timeout_seconds": int64(req.Msg.Setup.TimeoutSeconds), "plugins.files.skip": req.Msg.Setup.Skip}
	for key, value := range changes {
		parts := strings.Split(key, ".")
		if err := s.Plugins.Registry.Options().ValidateRepositorySetting(parts[0]+"."+parts[1], parts[2], value); err != nil {
			return nil, invalid(err.Error())
		}
	}
	revision := uint32(current.Settings.Revision)
	completed := []string{}
	for _, key := range []string{"plugins.setup.run", "plugins.setup.trust", "plugins.setup.timeout_seconds", "plugins.files.skip"} {
		revision, err = s.setRepositorySetting(ctx, req.Msg.RepositoryId, key, setting(changes[key]), key == "plugins.setup.run" && req.Msg.Setup.Run == "", revision)
		if err != nil {
			return nil, partial(err, "", completed...)
		}
		completed = append(completed, "updated "+key)
	}
	out, err := s.GetCheckoutSetup(ctx, connect.NewRequest(&v1.GetCheckoutSetupRequest{RepositoryId: req.Msg.RepositoryId}))
	if err != nil {
		return nil, err
	}
	return response(&v1.UpdateCheckoutSetupResponse{Setup: out.Msg.Setup, Revision: out.Msg.Revision}, nil)
}
func repositoryFile(f repofiles.File) *v1.RepositoryFile {
	return &v1.RepositoryFile{Path: f.Path, Size: uint32(f.Size), Sha256: f.SHA256, ModifiedAt: stamp(f.UpdatedAt)}
}
func (s *Server) ListRepositoryFiles(ctx context.Context, req *connect.Request[v1.ListRepositoryFilesRequest]) (*connect.Response[v1.ListRepositoryFilesResponse], error) {
	value, err := s.Local.RepositoryFiles(ctx, req.Msg.RepositoryId)
	out := &v1.ListRepositoryFilesResponse{Files: []*v1.RepositoryFile{}}
	for _, f := range value.Files {
		out.Files = append(out.Files, repositoryFile(f))
	}
	return response(out, err)
}
func (s *Server) ReadRepositoryFile(ctx context.Context, req *connect.Request[v1.ReadRepositoryFileRequest]) (*connect.Response[v1.ReadRepositoryFileResponse], error) {
	value, err := s.Local.ReadRepositoryFile(ctx, req.Msg.RepositoryId, req.Msg.Path)
	return response(&v1.ReadRepositoryFileResponse{File: repositoryFile(value.File), Content: value.Content}, err)
}
func (s *Server) PutRepositoryFile(ctx context.Context, req *connect.Request[v1.PutRepositoryFileRequest]) (*connect.Response[v1.PutRepositoryFileResponse], error) {
	file, err := s.Local.PutRepositoryFile(ctx, req.Msg.RepositoryId, req.Msg.Path, req.Msg.Content)
	if err != nil {
		return nil, rpcError(err)
	}
	warnings, err := s.App.SyncRepositoryFiles(ctx, req.Msg.RepositoryId)
	if err != nil {
		return nil, partial(err, "", "stored repository file")
	}
	return response(&v1.PutRepositoryFileResponse{File: repositoryFile(file), Warnings: warnings}, nil)
}
func (s *Server) DeleteRepositoryFile(ctx context.Context, req *connect.Request[v1.DeleteRepositoryFileRequest]) (*connect.Response[v1.DeleteRepositoryFileResponse], error) {
	if err := s.Local.RemoveRepositoryFile(ctx, req.Msg.RepositoryId, req.Msg.Path); err != nil {
		return nil, rpcError(err)
	}
	warnings, err := s.App.SyncRepositoryFiles(ctx, req.Msg.RepositoryId)
	if err != nil {
		return nil, partial(err, "", "removed repository file")
	}
	return response(&v1.DeleteRepositoryFileResponse{Warnings: warnings}, nil)
}
func environmentVars(vars []model.EnvVar) []*v1.EnvironmentVariable {
	out := []*v1.EnvironmentVariable{}
	for _, v := range vars {
		out = append(out, &v1.EnvironmentVariable{Key: v.Key, Value: v.Value})
	}
	return out
}
func (s *Server) staleAgents(ctx context.Context, repositoryID string, changed *time.Time) []*v1.StaleAgent {
	out := []*v1.StaleAgent{}
	for _, a := range s.App.RepositoryAgents(ctx, repositoryID) {
		if changed != nil && a.StartedAt.Before(*changed) {
			out = append(out, &v1.StaleAgent{ItemId: a.WorkItemID, Title: a.Title, StartedAt: stamp(a.StartedAt), Busy: a.Busy})
		}
	}
	return out
}
func (s *Server) GetEnvironment(ctx context.Context, req *connect.Request[v1.GetEnvironmentRequest]) (*connect.Response[v1.GetEnvironmentResponse], error) {
	value, err := s.Local.RepositoryEnvironment(ctx, req.Msg.RepositoryId)
	return response(&v1.GetEnvironmentResponse{Variables: environmentVars(value.Environment.Vars), Revision: uint32(value.Environment.Revision), ChangedAt: optionalStamp(value.Environment.ChangedAt), StaleAgents: s.staleAgents(ctx, req.Msg.RepositoryId, value.Environment.ChangedAt)}, err)
}
func (s *Server) UpdateEnvironment(ctx context.Context, req *connect.Request[v1.UpdateEnvironmentRequest]) (*connect.Response[v1.UpdateEnvironmentResponse], error) {
	revision := uint64(req.Msg.ExpectedRevision)
	vars := []model.EnvVar{}
	for _, v := range req.Msg.Set {
		vars = append(vars, model.EnvVar{Key: v.Key, Value: v.Value})
	}
	value, err := s.Local.SetRepositoryEnvironment(ctx, coordinator.SetRepositoryEnvironmentCommand{Selector: req.Msg.RepositoryId, ExpectedRevision: &revision, Replace: req.Msg.Replace, Set: vars, Unset: req.Msg.Unset})
	if err != nil {
		return nil, rpcError(err)
	}
	s.Core.Database().NotifyAll()
	return response(&v1.UpdateEnvironmentResponse{Variables: environmentVars(value.Environment.Vars), Revision: uint32(value.Environment.Revision), StaleAgents: s.staleAgents(ctx, req.Msg.RepositoryId, value.Environment.ChangedAt)}, nil)
}
func (s *Server) SearchRepositoryFiles(ctx context.Context, req *connect.Request[v1.SearchRepositoryFilesRequest]) (*connect.Response[v1.SearchRepositoryFilesResponse], error) {
	source, err := s.Local.RepositorySource(ctx, req.Msg.RepositoryId, "")
	if err != nil {
		return nil, rpcError(err)
	}
	paths, err := findCommitFiles(ctx, source.GitDir, source.Commit, req.Msg.Query)
	if req.Msg.Limit > 0 && len(paths) > int(req.Msg.Limit) {
		paths = paths[:req.Msg.Limit]
	}
	return response(&v1.SearchRepositoryFilesResponse{Paths: paths}, err)
}
