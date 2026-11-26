package daemonapi_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/coordinator"
	"google.golang.org/protobuf/types/known/structpb"
)

// The daemon describes itself, its health, configuration, plugins and usage.
func TestSystemDescribesTheDaemon(t *testing.T) {
	t.Parallel()
	f := apitest.New(t)
	clients := f.Clients

	info := call(t, clients.System.GetInfo, &v1.GetInfoRequest{})
	if info.ApiVersion != "1.0" || info.DataRoot != f.DataRoot() || info.DefaultAgentProfile != "test" || info.StartedAt == nil || info.Caller.GetKind() != v1.CallerKind_CALLER_KIND_LOCAL_OPERATOR || info.Caller.GetPrincipal() != "user:operator" {
		t.Fatalf("info: %v", info)
	}
	if health := call(t, clients.System.GetHealth, &v1.GetHealthRequest{}); !health.Healthy || len(health.Problems) != 0 || health.LastObservationAt == nil {
		t.Fatalf("health: %v", health)
	}
	diagnostics := call(t, clients.System.RunDiagnostics, &v1.RunDiagnosticsRequest{}).Diagnostics
	var checks []string
	for _, diagnostic := range diagnostics {
		if !diagnostic.Ok {
			t.Errorf("diagnostic %s failed: %s", diagnostic.Check, diagnostic.Detail)
		}
		checks = append(checks, diagnostic.Check)
	}
	if !slices.Equal(checks, []string{"database", "observations"}) {
		t.Fatalf("diagnostics: %v", diagnostics)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(call(t, clients.System.GetConfig, &v1.GetConfigRequest{}).ConfigJson), &config); err != nil {
		t.Fatal(err)
	}
	if config["drivers"] == nil {
		t.Fatalf("config: %v", config)
	}
	plugins := map[string]*v1.Plugin{}
	for _, plugin := range call(t, clients.System.ListPlugins, &v1.ListPluginsRequest{}).Plugins {
		plugins[plugin.Name] = plugin
	}
	for _, name := range []string{"direnv", "setup", "files"} {
		if !plugins[name].GetBundled() {
			t.Errorf("bundled plugin %s: %v", name, plugins[name])
		}
	}
	if !slices.Contains(plugins["setup"].GetHooks(), "setup_checkout") {
		t.Errorf("the setup plugin's hooks: %v", plugins["setup"])
	}
	// The daemon answers usage from its plugin host; here the plugin is off.
	if usage := call(t, clients.System.GetUsage, &v1.GetUsageRequest{Wait: true}); usage.Enabled || usage.Error != "" || usage.CheckedAt == nil {
		t.Fatalf("usage: %v", usage)
	}
}

// Agent profiles, the default one and the title model are fenced by one
// revision, and each change invalidates every client's view.
func TestSystemAgentProfiles(t *testing.T) {
	t.Parallel()
	f := apitest.New(t)
	clients := f.Clients
	list := func() *v1.ListAgentProfilesResponse {
		t.Helper()
		return call(t, clients.Profiles.ListAgentProfiles, &v1.ListAgentProfilesRequest{})
	}
	initial := list()
	if initial.DefaultProfile != "test" || len(initial.Profiles) != 1 || initial.Profiles[0].Driver != "pi" {
		t.Fatalf("profiles: %v", initial)
	}
	if drivers := call(t, clients.Profiles.ListDrivers, &v1.ListDriversRequest{}).Drivers; !slices.ContainsFunc(drivers, func(d *v1.Driver) bool { return d.Name == "pi" }) {
		t.Fatalf("drivers: %v", drivers)
	}
	models := call(t, clients.Profiles.GetProfileModels, &v1.GetProfileModelsRequest{Name: "test"})
	if models.Driver != "pi" || len(models.Catalog.GetModels()) == 0 || models.Catalog.Models[0].Value != "test/model" || models.CatalogError != "" {
		t.Fatalf("profile models: %v", models)
	}
	refused(t, connect.CodeNotFound, clients.Profiles.GetProfileModels, &v1.GetProfileModelsRequest{Name: "missing"})

	stream, err := clients.Events.WatchChanges(t.Context(), connect.NewRequest(&v1.WatchChangesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if !stream.Receive() {
		t.Fatal(stream.Err())
	}
	invalidated := func(what string) {
		t.Helper()
		if !stream.Receive() || !stream.Msg().ProfilesChanged || !stream.Msg().All {
			t.Fatalf("%s did not invalidate the profiles: %v %v", what, stream.Msg(), stream.Err())
		}
	}

	config, _ := structpb.NewStruct(map[string]any{"model": "test/model"})
	put := call(t, clients.Profiles.PutAgentProfile, &v1.PutAgentProfileRequest{Profile: &v1.AgentProfile{Name: "other", Driver: "pi", Label: "Other", Config: config}, ExpectedRevision: initial.Revision})
	if put.Profile.Name != "other" || put.Revision == initial.Revision {
		t.Fatalf("put: %v", put)
	}
	invalidated("a new profile")
	refused(t, connect.CodeAborted, clients.Profiles.PutAgentProfile, &v1.PutAgentProfileRequest{Profile: &v1.AgentProfile{Name: "stale", Driver: "pi"}, ExpectedRevision: initial.Revision})
	// Zero is a revision like any other, not a way around the fence.
	refused(t, connect.CodeAborted, clients.Profiles.SetDefaultAgentProfile, &v1.SetDefaultAgentProfileRequest{Name: "other"})
	revision := call(t, clients.Profiles.SetDefaultAgentProfile, &v1.SetDefaultAgentProfileRequest{Name: "other", ExpectedRevision: put.Revision}).Revision
	invalidated("a new default")
	if got := list(); got.DefaultProfile != "other" || got.Revision != revision || len(got.Profiles) != 2 {
		t.Fatalf("after the default changed: %v", got)
	}
	refused(t, connect.CodeFailedPrecondition, clients.Profiles.DeleteAgentProfile, &v1.DeleteAgentProfileRequest{Name: "other", ExpectedRevision: revision})
	revision = call(t, clients.Profiles.SetTitleModel, &v1.SetTitleModelRequest{Model: &v1.ModelChoice{Driver: "pi", Model: "test/model", Reasoning: "low"}, ExpectedRevision: revision}).Revision
	invalidated("a title model")
	if got := list().TitleModel; got.GetDriver() != "pi" || got.GetModel() != "test/model" || got.GetReasoning() != "low" {
		t.Fatalf("title model: %v", got)
	}
	revision = call(t, clients.Profiles.SetDefaultAgentProfile, &v1.SetDefaultAgentProfileRequest{Name: "test", ExpectedRevision: revision}).Revision
	invalidated("the default restored")
	call(t, clients.Profiles.DeleteAgentProfile, &v1.DeleteAgentProfileRequest{Name: "other", ExpectedRevision: revision})
	invalidated("a deleted profile")
	if got := list(); len(got.Profiles) != 1 || got.Profiles[0].Name != "test" {
		t.Fatalf("after delete: %v", got)
	}
}

// A schedule starts an ordinary item, as the dashboard would, whose agent
// answers the schedule's rendered prompt.
func TestSystemSchedulesStartItemsLikeTheDashboard(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.WithAgents())
	clients := f.Clients
	repository := f.Repository(t, "test")
	profile := call(t, clients.Profiles.ListAgentProfiles, &v1.ListAgentProfilesRequest{}).DefaultProfile

	t.Run("invalid schedules are refused", func(t *testing.T) {
		for name, schedule := range map[string]*v1.Schedule{
			"bad cron":        {Cron: "every morning", Prompt: "p", AgentProfile: profile},
			"no prompt":       {Cron: "0 9 * * *", AgentProfile: profile},
			"refused setting": {Cron: "0 9 * * *", Prompt: "p", AgentProfile: profile, AgentSettings: map[string]string{"reasoning": "extreme"}},
		} {
			schedule.RepositoryId, schedule.Enabled = repository, true
			if _, err := clients.Schedules.PutSchedule(t.Context(), connect.NewRequest(&v1.PutScheduleRequest{Schedule: schedule})); connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Errorf("%s: %v", name, err)
			}
		}
		refused(t, connect.CodeNotFound, clients.Schedules.PutSchedule, &v1.PutScheduleRequest{Schedule: &v1.Schedule{RepositoryId: repository, Cron: "0 9 * * *", Prompt: "p", AgentProfile: "missing", Enabled: true}})
	})

	schedule := call(t, clients.Schedules.PutSchedule, &v1.PutScheduleRequest{Schedule: &v1.Schedule{RepositoryId: repository, Cron: "0 9 * * 1-5", Title: "Triage {{date}}", Prompt: "scheduled check of {{repo}}", AgentProfile: profile, AgentSettings: map[string]string{"reasoning": "high"}, Labels: []string{"nightly"}, Enabled: true}}).Schedule
	if schedule.Id == "" || schedule.NextRunAt == nil || schedule.Revision == 0 {
		t.Fatalf("put: %v", schedule)
	}
	item := call(t, clients.Schedules.RunSchedule, &v1.RunScheduleRequest{Id: schedule.Id}).ItemId
	messages := apitest.WaitHistory(t, clients, item, 2)
	apitest.WaitIdle(t, clients, item)
	if answer := messageText(messages[len(messages)-1]); answer != "Echo: scheduled check of test" {
		t.Fatalf("the agent's answer: %q", answer)
	}
	got := call(t, clients.Items.GetItem, &v1.GetItemRequest{ItemId: item, IncludeDescription: true})
	if got.Description != "scheduled check of test" || got.Item.State != v1.ItemState_ITEM_STATE_ACTIVE || got.Item.Title != "Triage "+time.Now().Format("2006-01-02") || got.Item.AgentProfile != profile || got.Item.AgentSettings["reasoning"] != "high" || !slices.Equal(got.Item.Labels, []string{"nightly"}) {
		t.Fatalf("scheduled item: %v", got)
	}

	listed := call(t, clients.Schedules.ListSchedules, &v1.ListSchedulesRequest{}).Schedules
	if len(listed) != 1 || listed[0].LastItemId != item || listed[0].LastRunAt == nil {
		t.Fatalf("after a run: %v", listed)
	}
	disabled := listed[0]
	disabled.Enabled = false
	disabled = call(t, clients.Schedules.PutSchedule, &v1.PutScheduleRequest{Schedule: disabled, ExpectedRevision: listed[0].Revision}).Schedule
	if disabled.Enabled || disabled.NextRunAt != nil || disabled.LastItemId != item {
		t.Fatalf("disabled: %v", disabled)
	}
	call(t, clients.Schedules.DeleteSchedule, &v1.DeleteScheduleRequest{Id: schedule.Id})
	if listed := call(t, clients.Schedules.ListSchedules, &v1.ListSchedulesRequest{}).Schedules; len(listed) != 0 {
		t.Fatalf("after delete: %v", listed)
	}
	if err := refused(t, connect.CodeNotFound, clients.Schedules.RunSchedule, &v1.RunScheduleRequest{Id: schedule.Id}); !strings.Contains(err.Error(), "schedule") {
		t.Fatalf("running a deleted schedule: %v", err)
	}
}

// A managed daemon belongs to its service manager: it refuses to be shut
// down through the API, says why, and keeps serving.
func TestManagedDaemonRefusesShutdown(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.WithConfig("[daemon]\nmanaged = true\n"))
	if info := call(t, f.Clients.System.GetInfo, &v1.GetInfoRequest{}); !info.Managed {
		t.Fatalf("daemon is not managed: %v", info)
	}
	err := refused(t, connect.CodeFailedPrecondition, f.Clients.System.Shutdown, &v1.ShutdownRequest{})
	if !strings.Contains(err.Error(), coordinator.ManagedDaemonMessage) {
		t.Fatalf("refusal does not say why: %v", err)
	}
	if health := call(t, f.Clients.System.GetHealth, &v1.GetHealthRequest{}); !health.Healthy {
		t.Fatalf("daemon stopped answering: %v", health)
	}
}
