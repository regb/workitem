package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/plugin"
	"github.com/regb/workitem/internal/usage"
	"github.com/regb/workitem/internal/version"
	webaccess "github.com/regb/workitem/internal/web"
)

func (b webBackend) GeneralSettings(ctx context.Context) (webaccess.GeneralSettings, error) {
	agents, err := b.Agents(ctx)
	return webaccess.GeneralSettings{Version: version.Current().Version, DefaultAgent: b.cfg.App.DefaultProfile(), Agents: agents, DataRoot: filepath.Dir(b.cfg.App.Store.WorktreesDir())}, err
}

func (b webBackend) Repositories(ctx context.Context) ([]webaccess.Repository, error) {
	views, err := b.cfg.Coordinator.ListRepositoryViews(ctx)
	if err != nil {
		return nil, err
	}
	repositories := make([]model.OwnedRepository, 0, len(views))
	for _, view := range views {
		repositories = append(repositories, view.OwnedRepository)
	}
	result := []webaccess.Repository{}
	for _, project := range webProjects(repositories) {
		for _, view := range views {
			if view.ID != project.ID {
				continue
			}
			files, err := b.cfg.Coordinator.RepositoryFiles(ctx, view.ID)
			if err != nil {
				return nil, err
			}
			repository := webaccess.Repository{ID: view.ID, Name: view.Name, IntegrationBranch: view.IntegrationBranch, CreatedAt: view.CreatedAt, Files: files.Files}
			if view.Upstream != nil {
				repository.Remote = webaccess.NewRemote(view.Upstream.URL, view.Upstream.Branch)
			}
			result = append(result, repository)
		}
	}
	return result, nil
}

func (b webBackend) Environment(ctx context.Context, project string) (webaccess.Environment, error) {
	result, err := b.cfg.Coordinator.RepositoryEnvironment(ctx, project)
	if err != nil {
		return webaccess.Environment{}, err
	}
	return b.environmentView(ctx, result), nil
}

func (b webBackend) SetEnvironment(ctx context.Context, project string, request webaccess.EnvironmentRequest) webaccess.Outcome {
	result, err := b.cfg.Coordinator.SetRepositoryEnvironment(ctx, coordinator.SetRepositoryEnvironmentCommand{Selector: project, ExpectedRevision: &request.Revision, Replace: true, Set: request.Vars})
	if err != nil {
		return outcome(nil, nil, err)
	}
	return outcome(b.environmentView(ctx, result), nil, nil)
}

func (b webBackend) environmentView(ctx context.Context, result coordinator.RepositoryEnvironmentResult) webaccess.Environment {
	view := webaccess.Environment{Project: webaccess.Project{ID: result.Repository.ID, Name: result.Repository.Name}, Vars: result.Environment.Vars, Revision: result.Environment.Revision, Agents: []webaccess.EnvironmentAgent{}}
	changed := result.Environment.ChangedAt
	for _, running := range b.cfg.App.RepositoryAgents(ctx, result.Repository.ID) {
		stale := changed != nil && running.StartedAt.Before(*changed)
		view.Agents = append(view.Agents, webaccess.EnvironmentAgent{ID: running.WorkItemID, Title: running.Title, StartedAt: running.StartedAt, Busy: running.Busy, Stale: stale})
	}
	return view
}

func (b webBackend) AddRepository(ctx context.Context, request webaccess.AddRepositoryRequest) webaccess.Outcome {
	added, err := onboardRepository(ctx, b.cfg, repositoryOnboarding{Name: request.Name, Source: request.Source, Branch: request.Branch})
	if err != nil {
		return webaccess.Outcome{Error: err.Error()}
	}
	return webaccess.Outcome{Result: webaccess.Project{ID: added.Record.ID, Name: added.Record.Name}}
}

func (b webBackend) AgentProfiles(ctx context.Context) (webaccess.AgentProfiles, error) {
	profiles, err := b.cfg.Coordinator.AgentProfiles(ctx)
	return b.agentProfileViews(profiles), err
}

func (b webBackend) agentProfileViews(profiles model.AgentProfiles) webaccess.AgentProfiles {
	result := webaccess.AgentProfiles{Revision: profiles.Revision, Default: profiles.Default, Profiles: []webaccess.AgentProfileView{}}
	for _, profile := range profiles.Profiles {
		result.Profiles = append(result.Profiles, webaccess.AgentProfileView{AgentProfile: profile, Users: b.cfg.App.AgentProfileUsers(profile.Name)})
	}
	return result
}

// ChangeAgentProfile applies one change, validating a profile with its
// driver first, as wi agent profile does.
func (b webBackend) ChangeAgentProfile(ctx context.Context, change webaccess.AgentProfileChange) webaccess.Outcome {
	if change.Put != nil {
		if err := b.cfg.App.ValidateAgentProfile(ctx, *change.Put); err != nil {
			return webaccess.Outcome{Error: err.Error()}
		}
	}
	profiles, err := b.cfg.Coordinator.ChangeAgentProfile(ctx, coordinator.ChangeAgentProfileCommand{ExpectedRevision: &change.Revision, Put: change.Put, Remove: change.Remove, Default: change.Default})
	if err != nil {
		return webaccess.Outcome{Error: err.Error()}
	}
	return webaccess.Outcome{Result: b.agentProfileViews(profiles)}
}

// settingUp tells the page that a start waits for a long setup.
const settingUp = "The workspace is still being set up; the agent starts when it is done."

// afterSetup runs then in the background once a setup that outlived its
// request has succeeded. A failed setup shows in the conversation instead.
func (b webBackend) afterSetup(ctx context.Context, id string, then func(context.Context)) {
	background := context.WithoutCancel(ctx)
	go func() {
		wait, cancel := context.WithTimeout(background, 3*time.Hour)
		defer cancel()
		if ready, err := b.cfg.App.WaitWorkspaceSetup(wait, b.resolve(id)); err != nil || !ready {
			return
		}
		run, cancel := context.WithTimeout(background, 2*time.Minute)
		defer cancel()
		then(run)
	}()
}

// continuesSetup reports an outcome cut short by a setup still running.
func continuesSetup(out webaccess.Outcome) bool {
	return strings.Contains(out.Error, app.ErrSetupContinues.Error())
}

// Usage runs the usage plugin, as wi usage --json does, and returns its
// results; it reports a plugin that is not enabled instead of failing.
func (b webBackend) Usage(ctx context.Context, refresh bool) (webaccess.Usage, error) {
	result := webaccess.Usage{Results: []usage.QuotaResult{}}
	host := b.cfg.Plugins
	if host == nil {
		return result, nil
	}
	specs, err := host.Specs(ctx, "")
	if err != nil {
		return result, err
	}
	for _, spec := range specs {
		if spec.Name != "usage" || !spec.Enabled {
			continue
		}
		result.Enabled = true
		args := []string{"--json", "show"}
		if refresh {
			args = append(args, "--refresh")
		}
		env := []string{}
		for key, value := range b.cfg.Env {
			if !strings.HasPrefix(key, "WI_PLUGIN") && key != "WI_EXECUTABLE" {
				env = append(env, key+"="+value)
			}
		}
		env = append(env, plugin.Variables(spec, host.SelfPath)...)
		var out, errs bytes.Buffer
		if err := plugin.RunCommand(ctx, spec, args, env, "", nil, &out, &errs); err != nil {
			return result, fmt.Errorf("the usage plugin failed: %s", strings.TrimSpace(errs.String()))
		}
		var snapshot usage.Snapshot
		if err := json.Unmarshal(out.Bytes(), &snapshot); err != nil {
			return result, fmt.Errorf("the usage plugin returned unreadable results")
		}
		result.Results = append(result.Results, snapshot.Results...)
	}
	return result, nil
}
