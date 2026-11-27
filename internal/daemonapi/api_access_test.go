package daemonapi_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/apitransport"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/credential"
	"github.com/regb/workitem/internal/daemonapi"
	"github.com/regb/workitem/internal/model"
)

// Who a call is from is the credential it presents, never a header; what it
// does is recorded as theirs.
func TestAccessPrincipals(t *testing.T) {
	t.Parallel()
	f := apitest.New(t)
	repo := f.Repository(t, "test")
	// info calls GetInfo with headers that claim another caller: they change
	// nothing.
	info := func(t *testing.T, clients *apitransport.Clients) (*v1.Caller, error) {
		t.Helper()
		request := connect.NewRequest(&v1.GetInfoRequest{})
		request.Header().Set(daemonapi.HolderHeader, "agent")
		request.Header().Set("Wi-Endpoint-Access", "operator")
		value, err := clients.System.GetInfo(t.Context(), request)
		if err != nil {
			return nil, err
		}
		return value.Msg.Caller, nil
	}

	t.Run("credentials name the caller", func(t *testing.T) {
		operator, err := info(t, f.Clients)
		if err != nil || operator.Kind != v1.CallerKind_CALLER_KIND_LOCAL_OPERATOR || operator.Principal != model.OperatorUser || operator.ItemId != "" {
			t.Fatalf("operator: %v %v", operator, err)
		}
		web, err := info(t, f.WebClients(t))
		if err != nil || web.Kind != v1.CallerKind_CALLER_KIND_WEB_ADAPTER || web.Principal != model.OperatorUser {
			t.Fatalf("web: %v %v", web, err)
		}
		forged, _ := credential.New(credential.Operator, "")
		for name, token := range map[string]string{"none": "", "garbage": "not a credential", "forged operator": forged.Token()} {
			clients := f.ClientsWith(t, func() (string, error) { return token, nil })
			if _, err := info(t, clients); connect.CodeOf(err) != connect.CodeUnauthenticated {
				t.Fatalf("%s: %v", name, err)
			}
		}
		item := newItem(t, f, repo, "agent identity").Id
		agent, runtime := f.AgentClients(t, item)
		caller, err := info(t, agent)
		if err != nil || caller.Kind != v1.CallerKind_CALLER_KIND_AGENT || caller.Principal != model.ItemPrincipal(item) || caller.ItemId != item {
			t.Fatalf("agent: %v %v", caller, err)
		}
		// Streams check the credential too.
		stream, err := agent.Events.WatchChanges(t.Context(), connect.NewRequest(&v1.WatchChangesRequest{}))
		if err != nil || !stream.Receive() {
			t.Fatalf("agent stream: %v %v", err, stream.Err())
		}
		stream.Close()
		// A new runtime taking the item over ends the old one's credential.
		newer, _ := f.AgentClients(t, item)
		if _, err := info(t, agent); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("replaced runtime: %v", err)
		}
		if _, err := info(t, newer); err != nil {
			t.Fatalf("current runtime: %v", err)
		}
		// White-box: a runtime asked to stop is refused before its host
		// records it stopped, a window the API cannot hold open.
		current, err := f.App().Store.LoadAgentRuntime(item)
		if err != nil || current == nil || current.ID == runtime.ID {
			t.Fatalf("runtime: %v %v", current, err)
		}
		current.State = model.AgentRuntimeStopping
		if err := f.App().Store.SaveAgentRuntime(t.Context(), item, *current); err != nil {
			t.Fatal(err)
		}
		if _, err := info(t, newer); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("stopping runtime: %v", err)
		}
		stream, err = newer.Events.WatchChanges(t.Context(), connect.NewRequest(&v1.WatchChangesRequest{}))
		if err == nil {
			stream.Receive()
			err = stream.Err()
			stream.Close()
		}
		if connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("stopping runtime stream: %v", err)
		}
	})

	t.Run("wi web is held to its exposure", func(t *testing.T) {
		web := f.WebClients(t)
		refused(t, connect.CodePermissionDenied, web.System.Shutdown, &v1.ShutdownRequest{})
		refused(t, connect.CodePermissionDenied, web.System.GetConfig, &v1.GetConfigRequest{})
		// The user wi web vouches for is the caller's label; only wi web's
		// credential makes the header believed.
		for _, c := range []struct {
			clients *apitransport.Clients
			label   string
		}{{web, "alice@example.com"}, {f.Clients, ""}} {
			request := connect.NewRequest(&v1.GetInfoRequest{})
			request.Header().Set(daemonapi.LabelHeader, "alice@example.com")
			got, err := c.clients.System.GetInfo(t.Context(), request)
			if err != nil || got.Msg.Caller.Label != c.label {
				t.Fatalf("label %q: %v %v", c.label, got, err)
			}
		}
	})

	t.Run("actions record who did them", func(t *testing.T) {
		parent := newItem(t, f, repo, "parent").Id
		agent, _ := f.AgentClients(t, parent)
		child := call(t, agent.Items.CreateItem, &v1.CreateItemRequest{RepositoryId: repo, Title: "child"}).Item.Id
		call(t, agent.Items.UpdateItem, &v1.UpdateItemRequest{ItemId: child, AddLabels: []string{"triage"}})
		call(t, agent.Items.MarkRead, &v1.MarkReadRequest{ItemId: child})
		seen := map[string]bool{}
		for _, event := range call(t, f.Clients.Items.ListItemEvents, &v1.ListItemEventsRequest{ItemId: child}).Events {
			seen[event.Type] = true
			if event.Actor != model.ItemPrincipal(parent) || event.ForUser != model.OperatorUser {
				t.Fatalf("%s recorded as %q for %q", event.Type, event.Actor, event.ForUser)
			}
		}
		if !seen["work_item.created"] || !seen["attention.read"] {
			t.Fatalf("events: %v", seen)
		}
		for _, event := range call(t, f.Clients.Items.ListItemEvents, &v1.ListItemEventsRequest{ItemId: parent}).Events {
			if event.Type == "work_item.created" && (event.Actor != model.OperatorUser || event.ForUser != model.OperatorUser) {
				t.Fatalf("operator's creation recorded as %q for %q", event.Actor, event.ForUser)
			}
		}
	})

	t.Run("a create-and-start names its repository", func(t *testing.T) {
		err := refused(t, connect.CodeInvalidArgument, f.Clients.Lifecycle.CreateAndStartItem, &v1.CreateAndStartItemRequest{Item: &v1.CreateItemRequest{RepositoryId: "not-an-id", Title: "x"}})
		if !strings.Contains(err.Error(), "repository_id") {
			t.Fatal(err)
		}
	})

	// Last: it replaces the credentials the other subtests use.
	t.Run("rotating credentials", func(t *testing.T) {
		before, err := credential.Read(credential.OperatorFile(f.DataRoot()), credential.Operator)
		if err != nil {
			t.Fatal(err)
		}
		stale := f.ClientsWith(t, func() (string, error) { return before.Token(), nil })
		call(t, stale.System.GetInfo, &v1.GetInfoRequest{})
		call(t, f.Clients.System.RotateCredentials, &v1.RotateCredentialsRequest{})
		refused(t, connect.CodeUnauthenticated, stale.System.GetInfo, &v1.GetInfoRequest{})
		// Clients reading the files carry on.
		call(t, f.Clients.System.GetInfo, &v1.GetInfoRequest{})
		call(t, f.WebClients(t).System.GetInfo, &v1.GetInfoRequest{})
	})
}

// An item's agent may do what its access says: everything on its own item
// and its delegations, and on others what entries grant, up to a level.
func TestAccessLevels(t *testing.T) {
	t.Parallel()
	f := apitest.New(t)
	repo := f.Repository(t, "test")
	a := newItem(t, f, repo, "a").Id
	b, _ := activeItem(t, f, repo, "b")
	agent, _ := f.AgentClients(t, a)
	c := call(t, agent.Items.CreateItem, &v1.CreateItemRequest{RepositoryId: repo, Title: "c"}).Item.Id
	denied := func(t *testing.T, needed v1.AccessLevel, err error) {
		t.Helper()
		var refusal *connect.Error
		if connect.CodeOf(err) != connect.CodePermissionDenied || !errors.As(err, &refusal) {
			t.Fatalf("want permission denied, got %v", err)
		}
		if needed == v1.AccessLevel_ACCESS_LEVEL_UNSPECIFIED {
			return
		}
		if detail, _ := refusal.Details()[0].Value(); detail.(*v1.AccessDenied).Needed != needed {
			t.Fatalf("needed %v, detail %v", needed, detail)
		}
	}
	anyLevel := v1.AccessLevel_ACCESS_LEVEL_UNSPECIFIED

	// By default every item's agent may manage every item.
	call(t, agent.Items.GetItem, &v1.GetItemRequest{ItemId: b.Id})
	call(t, f.Clients.Items.RevokeAccess, &v1.RevokeAccessRequest{EntryId: coordinator.DefaultAccessID})

	t.Run("without access", func(t *testing.T) {
		_, err := agent.Items.GetItem(t.Context(), connect.NewRequest(&v1.GetItemRequest{ItemId: b.Id}))
		denied(t, anyLevel, err)
		// Requests naming their item inside a nested request are held to it.
		_, err = agent.Reviews.GetRevisionPatch(t.Context(), connect.NewRequest(&v1.GetRevisionPatchRequest{Comparison: &v1.GetRevisionReviewRequest{ItemId: b.Id, RevisionId: "r1"}}))
		denied(t, v1.AccessLevel_ACCESS_LEVEL_VIEW, err)
		_, err = agent.Reviews.GetRevisionReviewFile(t.Context(), connect.NewRequest(&v1.GetRevisionReviewFileRequest{Comparison: &v1.GetRevisionReviewRequest{ItemId: b.Id, RevisionId: "r1"}, Path: "a"}))
		denied(t, anyLevel, err)
		refused(t, connect.CodeInvalidArgument, agent.Reviews.GetRevisionPatch, &v1.GetRevisionPatchRequest{})
		// Lists, jobs and the change feed name only what it may view.
		for _, row := range call(t, agent.Items.ListItems, &v1.ListItemsRequest{}).Items {
			if row.Id == b.Id {
				t.Fatal("the agent's list shows an item it may not view")
			}
		}
		jobs := call(t, f.Clients.Jobs.ListJobs, &v1.ListJobsRequest{ItemId: b.Id}).Jobs
		if len(jobs) == 0 {
			t.Fatal("b's checkout job is not listed")
		}
		if visible := call(t, agent.Jobs.ListJobs, &v1.ListJobsRequest{}).Jobs; accessHasJob(visible, jobs[0].Id) {
			t.Fatalf("the agent lists b's job: %v", visible)
		}
		refused(t, connect.CodeNotFound, agent.Jobs.GetJob, &v1.GetJobRequest{JobId: jobs[0].Id})
		changes, err := agent.Events.WatchChanges(t.Context(), connect.NewRequest(&v1.WatchChangesRequest{}))
		if err != nil || !changes.Receive() {
			t.Fatalf("changes: %v", err)
		}
		defer changes.Close()
		call(t, f.Clients.Items.AddNote, &v1.AddNoteRequest{ItemId: b.Id, Text: "hidden"})
		call(t, f.Clients.Items.AddNote, &v1.AddNoteRequest{ItemId: c, Text: "shown"})
		for seenC := false; !seenC; {
			if !changes.Receive() {
				t.Fatalf("the change feed ended: %v", changes.Err())
			}
			for _, id := range changes.Msg().ItemIds {
				if id == b.Id {
					t.Fatal("the agent heard of a change to an item it may not view")
				}
				seenC = seenC || id == c
			}
		}
		// Its own repository's environment it already runs with; another's,
		// and the profiles' settings, are a person's.
		call(t, agent.Repositories.GetEnvironment, &v1.GetEnvironmentRequest{RepositoryId: repo})
		_, err = agent.Repositories.GetEnvironment(t.Context(), connect.NewRequest(&v1.GetEnvironmentRequest{RepositoryId: "01M8CA5QR6R1R68TKH4QFXKP1H"}))
		denied(t, anyLevel, err)
		profiles := call(t, agent.Profiles.ListAgentProfiles, &v1.ListAgentProfilesRequest{}).Profiles
		if len(profiles) == 0 {
			t.Fatal("no profiles")
		}
		for _, p := range profiles {
			if p.Config != nil {
				t.Fatalf("an agent read profile %s's settings", p.Name)
			}
		}
		if mine := call(t, f.Clients.Profiles.ListAgentProfiles, &v1.ListAgentProfilesRequest{}); mine.Profiles[0].Config == nil {
			t.Fatal("the operator cannot read profile settings")
		}
	})

	t.Run("own item and delegations", func(t *testing.T) {
		for _, id := range []string{a, c} {
			call(t, agent.Items.UpdateItem, &v1.UpdateItemRequest{ItemId: id, AddLabels: []string{"mine"}})
		}
	})

	t.Run("grants reach as far as their level", func(t *testing.T) {
		call(t, f.Clients.Items.GrantAccess, &v1.GrantAccessRequest{Subject: model.ItemPrincipal(a), OnItemId: b.Id, Level: v1.AccessLevel_ACCESS_LEVEL_VIEW})
		call(t, agent.Items.GetItem, &v1.GetItemRequest{ItemId: b.Id})
		_, err := agent.Items.AddNote(t.Context(), connect.NewRequest(&v1.AddNoteRequest{ItemId: b.Id, Text: "hi"}))
		denied(t, anyLevel, err)
		call(t, f.Clients.Items.GrantAccess, &v1.GrantAccessRequest{Subject: "item:*", OnItemId: "*", Level: v1.AccessLevel_ACCESS_LEVEL_MESSAGE})
		call(t, agent.Items.AddNote, &v1.AddNoteRequest{ItemId: b.Id, Text: "hi"})
		_, err = agent.Items.UpdateItem(t.Context(), connect.NewRequest(&v1.UpdateItemRequest{ItemId: b.Id, AddLabels: []string{"x"}}))
		denied(t, anyLevel, err)
		// A message to a backlog item would start it, which takes control.
		backlog := newItem(t, f, repo, "backlog").Id
		_, err = agent.Agents.SendMessage(t.Context(), connect.NewRequest(&v1.SendMessageRequest{ItemId: backlog, Text: "start please"}))
		denied(t, v1.AccessLevel_ACCESS_LEVEL_CONTROL, err)
		_, err = agent.Lifecycle.CloseItem(t.Context(), connect.NewRequest(&v1.CloseItemRequest{ItemId: b.Id, Resolution: v1.Resolution_RESOLUTION_COMPLETED}))
		denied(t, anyLevel, err)
		// Streams are held to the same access: message includes viewing.
		stream, err := agent.Agents.WatchLiveContent(t.Context(), connect.NewRequest(&v1.WatchLiveContentRequest{ItemId: b.Id}))
		if err == nil {
			stream.Receive()
			err = stream.Err()
			stream.Close()
		}
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			t.Fatalf("live content with message access: %v", err)
		}
		entries := call(t, f.Clients.Items.ListAccess, &v1.ListAccessRequest{}).Entries
		if len(entries) != 2 {
			t.Fatalf("entries: %v", entries)
		}
		call(t, f.Clients.Items.RevokeAccess, &v1.RevokeAccessRequest{EntryId: entries[0].Id})
		if left := call(t, f.Clients.Items.ListAccess, &v1.ListAccessRequest{}).Entries; len(left) != 1 || left[0].Id != entries[1].Id {
			t.Fatalf("after revoking: %v", left)
		}
	})

	t.Run("decisions are a person's", func(t *testing.T) {
		for name, err := range map[string]func() error{
			"list access": func() error {
				_, err := agent.Items.ListAccess(t.Context(), connect.NewRequest(&v1.ListAccessRequest{}))
				return err
			},
			"grant": func() error {
				_, err := agent.Items.GrantAccess(t.Context(), connect.NewRequest(&v1.GrantAccessRequest{Subject: model.ItemPrincipal(a), OnItemId: "*", Level: v1.AccessLevel_ACCESS_LEVEL_MANAGE}))
				return err
			},
			"delete": func() error {
				_, err := agent.Items.DeleteItem(t.Context(), connect.NewRequest(&v1.DeleteItemRequest{ItemId: b.Id, Confirm: true}))
				return err
			},
			"cancel a job": func() error {
				_, err := agent.Jobs.CancelJob(t.Context(), connect.NewRequest(&v1.CancelJobRequest{JobId: "any"}))
				return err
			},
			"shutdown": func() error {
				_, err := agent.System.Shutdown(t.Context(), connect.NewRequest(&v1.ShutdownRequest{}))
				return err
			},
			"secret configuration": func() error {
				_, err := agent.System.GetConfig(t.Context(), connect.NewRequest(&v1.GetConfigRequest{ShowSensitive: true}))
				return err
			},
			"secret repository settings": func() error {
				_, err := agent.Repositories.GetRepositorySettings(t.Context(), connect.NewRequest(&v1.GetRepositorySettingsRequest{RepositoryId: repo, ShowSensitive: true}))
				return err
			},
			"reset to upstream": func() error {
				_, err := agent.Repositories.SyncRepository(t.Context(), connect.NewRequest(&v1.SyncRepositoryRequest{RepositoryId: repo, Mode: v1.SyncMode_SYNC_MODE_RESET_TO_UPSTREAM, Confirm: true}))
				return err
			},
		} {
			if connect.CodeOf(err()) != connect.CodePermissionDenied {
				t.Errorf("%s by an agent was not denied", name)
			}
		}
	})
}

func accessHasJob(jobs []*v1.Job, id string) bool {
	for _, job := range jobs {
		if job.Id == id {
			return true
		}
	}
	return false
}

// An item's agent delegates what it creates: the item is its agent's to
// follow up, out of the user's list, until the user takes it or the owning
// item closes and gives it back.
func TestAccessDelegation(t *testing.T) {
	t.Parallel()
	f := apitest.New(t)
	repo := f.Repository(t, "test")
	parent := newItem(t, f, repo, "parent")
	if parent.Owner != model.OperatorUser {
		t.Fatalf("the operator's item is owned by %q", parent.Owner)
	}
	setState := func(id string, state v1.ItemState, resolution v1.Resolution) {
		t.Helper()
		call(t, f.Clients.Items.SetItemState, &v1.SetItemStateRequest{ItemId: id, State: state, Resolution: resolution})
	}
	setState(parent.Id, v1.ItemState_ITEM_STATE_ACTIVE, 0)
	agent, _ := f.AgentClients(t, parent.Id)
	var delegated []string
	for _, title := range []string{"child 0", "child 1", "child 2"} {
		child := call(t, agent.Items.CreateItem, &v1.CreateItemRequest{RepositoryId: repo, Title: title}).Item
		if child.Owner != model.ItemPrincipal(parent.Id) {
			t.Fatalf("delegated item owned by %q", child.Owner)
		}
		delegated = append(delegated, child.Id)
	}
	list := func(owner string) map[string]bool {
		t.Helper()
		ids := map[string]bool{}
		for _, row := range call(t, f.Clients.Items.ListItems, &v1.ListItemsRequest{Owner: owner}).Items {
			ids[row.Id] = true
		}
		return ids
	}
	owner := func(id string) string {
		return call(t, f.Clients.Items.GetItem, &v1.GetItemRequest{ItemId: id}).Item.Owner
	}

	t.Run("the owner's list, not the user's", func(t *testing.T) {
		if mine := list(""); !mine[parent.Id] || mine[delegated[0]] {
			t.Fatalf("the user's list: %v", mine)
		}
		if theirs := list(model.ItemPrincipal(parent.Id)); len(theirs) != len(delegated) {
			t.Fatalf("the parent's delegations: %v", theirs)
		}
		// Whatever its owner's state, a delegation's attention goes to its
		// owner.
		setState(parent.Id, v1.ItemState_ITEM_STATE_BACKLOG, 0)
		if list("")[delegated[0]] {
			t.Fatal("a shelved owner's delegation is in the user's list")
		}
		setState(parent.Id, v1.ItemState_ITEM_STATE_ACTIVE, 0)
	})

	t.Run("rights reach down the owner chain", func(t *testing.T) {
		childAgent, _ := f.AgentClients(t, delegated[1])
		grandchild := call(t, childAgent.Items.CreateItem, &v1.CreateItemRequest{RepositoryId: repo, Title: "grandchild"}).Item.Id
		call(t, f.Clients.Items.RevokeAccess, &v1.RevokeAccessRequest{EntryId: coordinator.DefaultAccessID})
		call(t, agent.Items.UpdateItem, &v1.UpdateItemRequest{ItemId: grandchild, AddLabels: []string{"x"}})
		setState(grandchild, v1.ItemState_ITEM_STATE_CLOSED, v1.Resolution_RESOLUTION_ABANDONED)
	})

	t.Run("only a person changes an owner", func(t *testing.T) {
		refused(t, connect.CodePermissionDenied, agent.Items.UpdateItem, &v1.UpdateItemRequest{ItemId: delegated[0], Owner: model.OperatorUser})
		call(t, f.Clients.Items.UpdateItem, &v1.UpdateItemRequest{ItemId: delegated[0], Owner: model.OperatorUser})
		if !list("")[delegated[0]] {
			t.Fatal("a taken item is not in the user's list")
		}
		// A person hands an item to another item; no item owns itself, or an
		// item that owns it.
		call(t, f.Clients.Items.UpdateItem, &v1.UpdateItemRequest{ItemId: delegated[0], Owner: model.ItemPrincipal(delegated[1])})
		if got := owner(delegated[0]); got != model.ItemPrincipal(delegated[1]) {
			t.Fatalf("handed over to %q", got)
		}
		if _, err := f.Clients.Items.UpdateItem(t.Context(), connect.NewRequest(&v1.UpdateItemRequest{ItemId: delegated[1], Owner: model.ItemPrincipal(delegated[1])})); err == nil {
			t.Fatal("an item owns itself")
		}
		if _, err := f.Clients.Items.UpdateItem(t.Context(), connect.NewRequest(&v1.UpdateItemRequest{ItemId: parent.Id, Owner: model.ItemPrincipal(delegated[0])})); err == nil {
			t.Fatal("an item owns its own owner")
		}
		call(t, f.Clients.Items.UpdateItem, &v1.UpdateItemRequest{ItemId: delegated[0], Owner: model.OperatorUser})
	})

	t.Run("closing the owner gives the rest back, unread", func(t *testing.T) {
		setState(parent.Id, v1.ItemState_ITEM_STATE_CLOSED, v1.Resolution_RESOLUTION_COMPLETED)
		eventually(t, "the delegations to return", func() bool {
			for _, id := range delegated {
				if owner(id) != model.OperatorUser {
					return false
				}
			}
			return true
		})
		if got := call(t, f.Clients.Items.GetItem, &v1.GetItemRequest{ItemId: delegated[1]}); !got.Observation.Unread {
			t.Fatalf("returned item is read: %v", got)
		}
		if _, err := agent.Items.CreateItem(t.Context(), connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "after close"})); err == nil {
			t.Fatal("a closed item delegated")
		}
	})
}

// Agents talk through the daemon: a message from another item's agent comes
// in an envelope naming its sender and how it relates; an owning item's
// agent hears of its delegations' turns, unless it awaited the reply; a
// stopped runtime's credential is refused.
func TestAccessAgentsMessagesAndNotices(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.WithAgents())
	repo := f.Repository(t, "test")
	parentItem, _ := activeItem(t, f, repo, "Parent")
	parent := parentItem.Id
	startAgent(t, f, parent)
	other, _ := activeItem(t, f, repo, "Other")
	startAgent(t, f, other.Id)
	asParent := accessRuntimeClients(t, f, parent)
	if caller := call(t, asParent.System.GetInfo, &v1.GetInfoRequest{}).Caller; caller.Kind != v1.CallerKind_CALLER_KIND_AGENT || caller.ItemId != parent {
		t.Fatalf("the parent's runtime credential: %v", caller)
	}
	notices := func() []string {
		var out []string
		for _, m := range call(t, f.Clients.Agents.GetHistory, &v1.GetHistoryRequest{ItemId: parent}).Messages {
			if text := messageText(m); m.Role == v1.MessageRole_MESSAGE_ROLE_USER && strings.HasPrefix(text, "wi notice:") {
				out = append(out, text)
			}
		}
		return out
	}
	awaitNotice := func(count int, contains string) {
		t.Helper()
		eventually(t, "notice "+contains, func() bool {
			got := notices()
			return len(got) >= count && strings.Contains(got[count-1], contains)
		})
	}
	// ask sends text to item as from, awaiting the reply as wi send --wait
	// does, and returns the reply.
	ask := func(t *testing.T, from *apitransport.Clients, item, text string) string {
		t.Helper()
		before := len(call(t, f.Clients.Agents.GetHistory, &v1.GetHistoryRequest{ItemId: item}).Messages)
		accessSend(t, from, &v1.SendMessageRequest{ItemId: item, Text: text, AwaitsReply: true})
		messages := apitest.WaitHistory(t, f.Clients, item, before+2)
		apitest.WaitIdle(t, f.Clients, item)
		return messageText(messages[len(messages)-1])
	}

	// The parent delegates with a task: when the child's first turn ends,
	// the parent hears of it with the child's reply.
	start, err := asParent.Lifecycle.CreateAndStartItem(t.Context(), accessAsCLI(&v1.CreateAndStartItemRequest{Item: &v1.CreateItemRequest{RepositoryId: repo, Title: "Child"}, Start: &v1.StartOptions{Prompt: "do the thing", Mode: v1.AgentMode_AGENT_MODE_HEADLESS}}))
	if err != nil {
		t.Fatal(err)
	}
	created := start.Msg
	child := created.Item.Id
	if created.Item.Owner != model.ItemPrincipal(parent) {
		t.Fatalf("the child is owned by %q", created.Item.Owner)
	}
	awaitNotice(1, "finished its turn")
	if got := notices()[0]; !strings.Contains(got, child) || !strings.Contains(got, "do the thing") {
		t.Fatalf("notice: %s", got)
	}
	apitest.WaitIdle(t, f.Clients, parent)
	apitest.WaitIdle(t, f.Clients, child)

	t.Run("envelopes", func(t *testing.T) {
		// The fake agent echoes what it received: the envelope wi wrote.
		envelope := func(answer, from, title, relation string) {
			t.Helper()
			for _, part := range []string{"Echo: <wi-message-", `from="` + from + `"`, `title="` + title + `"`, `relation="` + relation + `"`} {
				if !strings.Contains(answer, part) {
					t.Fatalf("answer lacks %s: %s", part, answer)
				}
			}
		}
		envelope(ask(t, asParent, child, "ping"), model.ItemPrincipal(parent), "Parent", model.RelationOwner)
		envelope(ask(t, asParent, other.Id, "Do the checks pass?"), model.ItemPrincipal(parent), "Parent", model.RelationOther)
		asChild := accessRuntimeClients(t, f, child)
		envelope(ask(t, asChild, parent, "status"), model.ItemPrincipal(child), "Child", model.RelationDelegation)
		// The receiving history shows the sender and the text, without the
		// envelope.
		found := false
		for _, m := range call(t, f.Clients.Agents.GetHistory, &v1.GetHistoryRequest{ItemId: other.Id}).Messages {
			if m.Sender == model.ItemPrincipal(parent) {
				found = true
				if text := messageText(m); text != "Do the checks pass?" {
					t.Fatalf("history kept the envelope: %q", text)
				}
			}
		}
		if !found {
			t.Fatal("no message from the parent in the other item's history")
		}
		// A person's message is not wrapped, and names them.
		if answer := ask(t, f.Clients, other.Id, "From you"); answer != "Echo: From you" {
			t.Fatalf("the operator's message: %q", answer)
		}
		last := ""
		for _, m := range call(t, f.Clients.Agents.GetHistory, &v1.GetHistoryRequest{ItemId: other.Id}).Messages {
			if m.Role == v1.MessageRole_MESSAGE_ROLE_USER && m.Sender != "" {
				last = m.Sender
			}
		}
		if last != model.OperatorUser {
			t.Fatalf("the operator's message sender: %q", last)
		}
	})

	t.Run("notices", func(t *testing.T) {
		// The turns the parent awaited above sent no notice: the next one is
		// the child's question.
		accessSend(t, f.Clients, &v1.SendMessageRequest{ItemId: child, Text: "please ask me something"})
		awaitNotice(2, "waiting on a question")
		if got := notices(); len(got) != 2 {
			t.Fatalf("an awaited turn was noticed: %q", got)
		}
		// A stopped runtime's credential is refused; an owner whose agent
		// stopped still hears, as the notice starts it.
		apitest.WaitIdle(t, f.Clients, parent)
		call(t, f.Clients.Agents.StopAgent, &v1.StopAgentRequest{ItemId: parent, Force: true})
		refused(t, connect.CodeUnauthenticated, asParent.System.GetInfo, &v1.GetInfoRequest{})
		question := accessPendingQuestion(t, f, child)
		// Answered as wi agent answer does, a command line, which gives
		// control back once delivered.
		if _, err := f.Clients.Agents.AnswerQuestion(t.Context(), accessAsCLI(&v1.AnswerQuestionRequest{ItemId: child, ToolCallId: question, Answers: map[string]string{"Which color?": "Red"}})); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the notice of the answered turn", func() bool {
			for _, notice := range notices() {
				if strings.Contains(notice, "Chosen: Red") {
					return true
				}
			}
			return false
		})
		if agent := apitest.WaitIdle(t, f.Clients, parent); !agent.Online {
			t.Fatal("the notice did not start the owner's agent")
		}
	})

	// Last: the child's agent then holds control of its own item.
	t.Run("an agent's message to its own item is not wrapped", func(t *testing.T) {
		apitest.WaitIdle(t, f.Clients, child)
		if answer := ask(t, accessRuntimeClients(t, f, child), child, "to myself"); answer != "Echo: to myself" {
			t.Fatalf("an agent's message to its own item: %q", answer)
		}
	})
}

// accessRuntimeClients connects with the credential the daemon gave item's
// running agent, as that agent's wi commands do.
func accessRuntimeClients(t *testing.T, f *apitest.Fixture, item string) *apitransport.Clients {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(f.Paths.DataStateRoot, "items", item, "runtimes", "*", "credential"))
	if len(paths) != 1 {
		t.Fatalf("the runtime credentials of %s: %v", item, paths)
	}
	value, err := credential.Read(paths[0], credential.Item)
	if err != nil {
		t.Fatal(err)
	}
	return f.ClientsWith(t, func() (string, error) { return value.Token(), nil })
}

// accessAsCLI makes a request as the wi command line does: a command line
// takes control only to deliver what it sends, handing it over between
// command lines.
func accessAsCLI[T any](message *T) *connect.Request[T] {
	request := connect.NewRequest(message)
	request.Header().Set(daemonapi.ClientHeader, "cli")
	request.Header().Set(daemonapi.HolderHeader, model.LeaseHolderCLI)
	return request
}

// accessSend sends a message as wi send does, with an ID: the daemon
// records its sender, and whether it awaits the reply, by that ID.
func accessSend(t *testing.T, clients *apitransport.Clients, message *v1.SendMessageRequest) {
	t.Helper()
	message.ClientMessageId = fmt.Sprintf("access-%d", time.Now().UnixNano())
	if _, err := clients.Agents.SendMessage(t.Context(), accessAsCLI(message)); err != nil {
		t.Fatalf("send to %s: %v", message.ItemId, err)
	}
}

// accessPendingQuestion returns the tool call item's agent waits on.
func accessPendingQuestion(t *testing.T, f *apitest.Fixture, item string) string {
	t.Helper()
	id := ""
	eventually(t, "the agent's question", func() bool {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		stream, err := f.Clients.Agents.WatchLiveContent(ctx, connect.NewRequest(&v1.WatchLiveContentRequest{ItemId: item}))
		if err != nil {
			return false
		}
		defer stream.Close()
		if stream.Receive() {
			id = stream.Msg().GetSnapshot().GetQuestion().GetId()
		}
		return id != ""
	})
	return id
}
