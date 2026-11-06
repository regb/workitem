package cli

import (
	"context"
	"strings"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apiview"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/repofiles"
	webaccess "github.com/regb/workitem/internal/web"
	"google.golang.org/protobuf/types/known/structpb"
)

func (b webBackend) GeneralSettings(ctx context.Context) (webaccess.GeneralSettings, error) {
	value, err := b.cfg.API.System.GetInfo(ctx, webRequest(ctx, &v1.GetInfoRequest{}))
	if err != nil {
		return webaccess.GeneralSettings{}, err
	}
	agents, err := b.Agents(ctx)
	return webaccess.GeneralSettings{Version: value.Msg.Version, DataRoot: value.Msg.DataRoot, DefaultAgent: value.Msg.DefaultAgentProfile, Agents: agents}, err
}
func (b webBackend) Repositories(ctx context.Context) ([]webaccess.Repository, error) {
	value, err := b.cfg.API.Repositories.ListRepositories(ctx, webRequest(ctx, &v1.ListRepositoriesRequest{}))
	if err != nil {
		return nil, err
	}
	out := []webaccess.Repository{}
	for _, repo := range value.Msg.Repositories {
		row := webaccess.Repository{ID: repo.Id, Name: repo.Name, IntegrationBranch: repo.IntegrationBranch, CreatedAt: apiview.Time(repo.CreatedAt), Files: []repofiles.File{}}
		if u := repo.Upstream; u != nil {
			row.Remote = webaccess.NewRemote(u.Url, u.Branch)
		}
		files, err := b.cfg.API.Repositories.ListRepositoryFiles(ctx, webRequest(ctx, &v1.ListRepositoryFilesRequest{RepositoryId: repo.Id}))
		if err != nil {
			return nil, err
		}
		for _, f := range files.Msg.Files {
			row.Files = append(row.Files, repofiles.File{Path: f.Path, Size: int64(f.Size), SHA256: f.Sha256, UpdatedAt: apiview.Time(f.ModifiedAt)})
		}
		out = append(out, row)
	}
	return out, nil
}
func (b webBackend) AgentProfiles(ctx context.Context) (webaccess.AgentProfiles, error) {
	value, err := b.cfg.API.Profiles.ListAgentProfiles(ctx, webRequest(ctx, &v1.ListAgentProfilesRequest{}))
	if err != nil {
		return webaccess.AgentProfiles{}, err
	}
	out := webaccess.AgentProfiles{Default: value.Msg.DefaultProfile, Revision: uint64(value.Msg.Revision), Profiles: []webaccess.AgentProfileView{}}
	for _, p := range value.Msg.Profiles {
		row := webaccess.AgentProfileView{AgentProfile: model.AgentProfile{Name: p.Name, Driver: p.Driver, Label: p.Label}, Users: int(p.Users)}
		if p.Config != nil {
			row.Config = p.Config.AsMap()
		}
		out.Profiles = append(out.Profiles, row)
	}
	return out, nil
}
func (b webBackend) ChangeAgentProfile(ctx context.Context, change webaccess.AgentProfileChange) webaccess.Outcome {
	var err error
	switch {
	case change.Put != nil:
		p := change.Put
		values, e := structpb.NewStruct(p.Config)
		if e != nil {
			return outcome(nil, nil, e)
		}
		_, err = b.cfg.API.Profiles.PutAgentProfile(ctx, webRequest(ctx, &v1.PutAgentProfileRequest{Profile: &v1.AgentProfile{Name: p.Name, Driver: p.Driver, Label: p.Label, Config: values}, ExpectedRevision: uint32(change.Revision)}))
	case change.Remove != "":
		_, err = b.cfg.API.Profiles.DeleteAgentProfile(ctx, webRequest(ctx, &v1.DeleteAgentProfileRequest{Name: change.Remove, ExpectedRevision: uint32(change.Revision)}))
	case change.Default != "":
		_, err = b.cfg.API.Profiles.SetDefaultAgentProfile(ctx, webRequest(ctx, &v1.SetDefaultAgentProfileRequest{Name: change.Default, ExpectedRevision: uint32(change.Revision)}))
	}
	if err != nil {
		return outcome(nil, nil, err)
	}
	value, err := b.AgentProfiles(ctx)
	return outcome(value, nil, err)
}
func (b webBackend) AddRepository(ctx context.Context, request webaccess.AddRepositoryRequest) webaccess.Outcome {
	value, err := b.cfg.API.Repositories.AddRepository(ctx, webRequest(ctx, &v1.AddRepositoryRequest{Name: request.Name, Source: request.Source, Branch: request.Branch}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	if value.Msg.Job != nil {
		return b.job(ctx, "", value.Msg.Job, "")
	}
	return outcome(value.Msg, nil, nil)
}
func (b webBackend) Environment(ctx context.Context, project string) (webaccess.Environment, error) {
	value, err := b.cfg.API.Repositories.GetEnvironment(ctx, webRequest(ctx, &v1.GetEnvironmentRequest{RepositoryId: project}))
	if err != nil {
		return webaccess.Environment{}, err
	}
	repo, err := b.cfg.API.Repositories.GetRepository(ctx, webRequest(ctx, &v1.GetRepositoryRequest{RepositoryId: project}))
	if err != nil {
		return webaccess.Environment{}, err
	}
	return webEnvironment(project, repo.Msg.Repository.Name, value.Msg.Variables, value.Msg.Revision, value.Msg.StaleAgents), nil
}
func webEnvironment(project, name string, vars []*v1.EnvironmentVariable, revision uint32, agents []*v1.StaleAgent) webaccess.Environment {
	out := webaccess.Environment{Project: webaccess.Project{ID: project, Name: name}, Revision: uint64(revision), Vars: []model.EnvVar{}, Agents: []webaccess.EnvironmentAgent{}}
	for _, v := range vars {
		out.Vars = append(out.Vars, model.EnvVar{Key: v.Key, Value: v.Value})
	}
	for _, a := range agents {
		out.Agents = append(out.Agents, webaccess.EnvironmentAgent{ID: a.ItemId, Title: a.Title, StartedAt: apiview.Time(a.StartedAt), Busy: a.Busy, Stale: true})
	}
	return out
}
func (b webBackend) SetEnvironment(ctx context.Context, project string, request webaccess.EnvironmentRequest) webaccess.Outcome {
	r := &v1.UpdateEnvironmentRequest{RepositoryId: project, ExpectedRevision: uint32(request.Revision), Replace: true}
	for _, v := range request.Vars {
		r.Set = append(r.Set, &v1.EnvironmentVariable{Key: v.Key, Value: v.Value})
	}
	_, err := b.cfg.API.Repositories.UpdateEnvironment(ctx, webRequest(ctx, r))
	if err != nil {
		return outcome(nil, nil, err)
	}
	value, err := b.Environment(ctx, project)
	return outcome(value, nil, err)
}
func (b webBackend) Settings(ctx context.Context, id string) (app.AgentSettingsView, error) {
	value, err := b.cfg.API.Agents.GetAgentSettings(ctx, webRequest(ctx, &v1.GetAgentSettingsRequest{ItemId: id}))
	if err != nil {
		return app.AgentSettingsView{}, err
	}
	out := apiview.Settings(value.Msg)
	out.WorkItemID = id
	// The page reads live as a list, even for an agent that is not running.
	out.Live = apiview.Strings(out.Live)
	if a, err := b.cfg.API.Agents.GetAgent(ctx, webRequest(ctx, &v1.GetAgentRequest{ItemId: id})); err == nil && a.Msg.Agent != nil {
		out.RuntimeOnline = a.Msg.Agent.Online
		out.RuntimeMode = apiview.Mode(a.Msg.Agent.Mode)
		if a.Msg.Agent.Capabilities != nil {
			out.Live = apiview.Strings(a.Msg.Agent.Capabilities.LiveSettings)
		}
	}
	return out, nil
}
func (b webBackend) SetSettings(ctx context.Context, id string, request webaccess.SettingsRequest) webaccess.Outcome {
	r := &v1.UpdateAgentSettingsRequest{ItemId: id, Settings: request.Settings, Restart: request.Restart}
	current, err := b.cfg.API.Agents.GetAgent(ctx, webRequest(ctx, &v1.GetAgentRequest{ItemId: id}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	if current.Msg.Agent.Online {
		control, err := b.cfg.API.Control.AcquireControl(ctx, webRequest(ctx, &v1.AcquireControlRequest{ItemId: id, ClientId: webaccess.ClientID(ctx)}))
		if err != nil {
			return outcome(nil, nil, err)
		}
		if control.Msg.Control.Lease != nil {
			r.LeaseId = control.Msg.Control.Lease.Id
		}
	}
	value, err := b.cfg.API.Agents.UpdateAgentSettings(ctx, webRequest(ctx, r))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return outcome(value.Msg, value.Msg.Warnings, nil)
}
func (b webBackend) Uncommitted(ctx context.Context, id string) (app.UncommittedChanges, error) {
	value, err := b.cfg.API.Workspaces.GetUncommittedChanges(ctx, webRequest(ctx, &v1.GetUncommittedChangesRequest{ItemId: id}))
	if err != nil {
		return app.UncommittedChanges{}, err
	}
	return app.UncommittedChanges{Files: value.Msg.Files, Attached: value.Msg.AttachedCopies}, nil
}
func (b webBackend) Start(ctx context.Context, id string, request webaccess.StartRequest) webaccess.Outcome {
	if out, pending := b.pendingApproval(ctx, id, request.Approval); pending {
		return out
	}
	value, err := b.cfg.API.Lifecycle.StartItem(ctx, webRequest(ctx, &v1.StartItemRequest{ItemId: id, Start: &v1.StartOptions{Mode: v1.AgentMode_AGENT_MODE_HEADLESS, ClientMessageId: request.ClientID}}))
	if err != nil {
		return b.observed(ctx, outcome(nil, nil, err), id)
	}
	return b.job(ctx, id, value.Msg.Job, request.Approval)
}
func (b webBackend) Setup(ctx context.Context, id string) webaccess.Outcome {
	return b.Start(ctx, id, webaccess.StartRequest{})
}
func (b webBackend) Restart(ctx context.Context, id string) webaccess.Outcome {
	value, err := b.cfg.API.Agents.RestartAgent(ctx, webRequest(ctx, &v1.RestartAgentRequest{ItemId: id}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return b.job(ctx, id, value.Msg.Job, "")
}
func (b webBackend) Shelve(ctx context.Context, id string, request webaccess.ShelveRequest) webaccess.Outcome {
	value, err := b.cfg.API.Lifecycle.ShelveItem(ctx, webRequest(ctx, &v1.ShelveItemRequest{ItemId: id, ConfirmAttachedTerminal: request.ConfirmAttachedTerminal}))
	if err != nil {
		return b.observed(ctx, outcome(nil, nil, err), id)
	}
	if value.Msg.Job != nil {
		return b.job(ctx, id, value.Msg.Job, "")
	}
	return b.observed(ctx, outcome(value.Msg, value.Msg.Warnings, nil), id)
}
func (b webBackend) Reopen(ctx context.Context, id string, request webaccess.ReopenRequest) webaccess.Outcome {
	if out, pending := b.pendingApproval(ctx, id, request.Approval); pending {
		return out
	}
	value, err := b.cfg.API.Lifecycle.ReopenItem(ctx, webRequest(ctx, &v1.ReopenItemRequest{ItemId: id, Start: request.Start, Mode: v1.AgentMode_AGENT_MODE_HEADLESS}))
	if err != nil {
		return b.observed(ctx, outcome(nil, nil, err), id)
	}
	if value.Msg.Job != nil {
		return b.job(ctx, id, value.Msg.Job, request.Approval)
	}
	return b.observed(ctx, outcome(value.Msg, nil, nil), id)
}
func (b webBackend) Close(ctx context.Context, id string, request webaccess.CloseRequest) webaccess.Outcome {
	value, err := b.cfg.API.Lifecycle.CloseItem(ctx, webRequest(ctx, &v1.CloseItemRequest{ItemId: id, Resolution: v1.Resolution(v1.Resolution_value["RESOLUTION_"+strings.ToUpper(request.Resolution)]), Force: request.Force, CommitChanges: request.CommitChanges, ConfirmAttachedTerminal: request.ConfirmAttachedTerminal}))
	if err != nil {
		return b.observed(ctx, outcome(nil, nil, err), id)
	}
	if value.Msg.Job != nil {
		return b.job(ctx, id, value.Msg.Job, "")
	}
	return b.observed(ctx, outcome(value.Msg, value.Msg.Warnings, nil), id)
}
