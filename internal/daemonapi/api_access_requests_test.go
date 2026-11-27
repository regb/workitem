package daemonapi_test

import (
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

// What an agent is refused for want of access waits for a person as a
// request, until it is granted or dismissed.
func TestAccessRequestsWaitForAPerson(t *testing.T) {
	t.Parallel()
	f := apitest.New(t)
	repo := f.Repository(t, "test")
	a := newItem(t, f, repo, "asker").Id
	b := newItem(t, f, repo, "wanted").Id
	c := newItem(t, f, repo, "dismissed").Id
	call(t, f.Clients.Items.RevokeAccess, &v1.RevokeAccessRequest{EntryId: coordinator.DefaultAccessID})
	agent, _ := f.AgentClients(t, a)
	subject := model.ItemPrincipal(a)
	refused(t, connect.CodePermissionDenied, agent.Items.AddNote, &v1.AddNoteRequest{ItemId: b, Text: "hello"})
	refused(t, connect.CodePermissionDenied, agent.Items.GetItem, &v1.GetItemRequest{ItemId: b})
	refused(t, connect.CodePermissionDenied, agent.Items.GetItem, &v1.GetItemRequest{ItemId: c})
	// A refusal no grant can lift is no request.
	refused(t, connect.CodePermissionDenied, agent.Items.DeleteItem, &v1.DeleteItemRequest{ItemId: b, Confirm: true})
	requests := call(t, f.Clients.Items.ListAccessRequests, &v1.ListAccessRequestsRequest{Subject: subject}).Requests
	if len(requests) != 2 {
		t.Fatalf("requests: %v", requests)
	}
	byItem := map[string]*v1.AccessRequest{}
	for _, r := range requests {
		byItem[r.ItemId] = r
	}
	// The highest level needed stands.
	if r := byItem[b]; r == nil || r.Needed != v1.AccessLevel_ACCESS_LEVEL_MESSAGE || r.Refusals != 2 || r.Subject != subject || r.LastRefusedAt == nil {
		t.Fatalf("request on the wanted item: %v", r)
	}
	call(t, f.Clients.Items.GrantAccess, &v1.GrantAccessRequest{Subject: subject, OnItemId: b, Level: v1.AccessLevel_ACCESS_LEVEL_MESSAGE})
	call(t, f.Clients.Items.DismissAccessRequest, &v1.DismissAccessRequestRequest{Subject: subject, ItemId: c})
	if left := call(t, f.Clients.Items.ListAccessRequests, &v1.ListAccessRequestsRequest{}).Requests; len(left) != 0 {
		t.Fatalf("after granting and dismissing: %v", left)
	}
	refused(t, connect.CodeNotFound, f.Clients.Items.DismissAccessRequest, &v1.DismissAccessRequestRequest{Subject: subject, ItemId: c})
	// Requests are a person's to read.
	refused(t, connect.CodePermissionDenied, agent.Items.ListAccessRequests, &v1.ListAccessRequestsRequest{})
}
