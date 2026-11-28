package daemonapi_test

import (
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/model"
)

// A person who mentions another item in a message to an agent lets that
// agent read it; a level it has is never lowered. CheckAccess says what an
// item's agent may do to items.
func TestMentionsLetTheAgentReadTheItem(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.WithAgents())
	repo := f.Repository(t, "test")
	agentItem, _ := activeItem(t, f, repo, "Composer")
	startAgent(t, f, agentItem.Id)
	mentioned := newItem(t, f, repo, "Mentioned")
	subject := model.ItemPrincipal(agentItem.Id)
	level := func() v1.AccessLevel {
		t.Helper()
		access := call(t, f.Clients.Items.CheckAccess, &v1.CheckAccessRequest{Subject: subject, ItemIds: []string{mentioned.Id, agentItem.Id}}).Access
		if len(access) != 2 || access[1].Level != v1.AccessLevel_ACCESS_LEVEL_MANAGE {
			t.Fatalf("access: %v", access)
		}
		return access[0].Level
	}
	if got := level(); got != v1.AccessLevel_ACCESS_LEVEL_UNSPECIFIED {
		t.Fatalf("before the mention: %v", got)
	}
	mention := "Look at @@[Mentioned](wi:" + mentioned.Id + ` "mentioned")`
	call(t, f.Clients.Agents.SendMessage, &v1.SendMessageRequest{ItemId: agentItem.Id, Text: mention})
	if got := level(); got != v1.AccessLevel_ACCESS_LEVEL_VIEW {
		t.Fatalf("after the mention: %v", got)
	}
	apitest.WaitIdle(t, f.Clients, agentItem.Id)
	call(t, f.Clients.Items.GrantAccess, &v1.GrantAccessRequest{Subject: subject, OnItemId: mentioned.Id, Level: v1.AccessLevel_ACCESS_LEVEL_MESSAGE})
	call(t, f.Clients.Agents.SendMessage, &v1.SendMessageRequest{ItemId: agentItem.Id, Text: mention})
	if got := level(); got != v1.AccessLevel_ACCESS_LEVEL_MESSAGE {
		t.Fatalf("a mention lowered the level to %v", got)
	}
	// Access is a person's to read.
	agent, _ := f.AgentClients(t, agentItem.Id)
	refused(t, connect.CodePermissionDenied, agent.Items.CheckAccess, &v1.CheckAccessRequest{Subject: subject, ItemIds: []string{mentioned.Id}})
}
