package daemonapi_test

import (
	"testing"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

// A message from another item's agent shows in history without its envelope
// and names its sender, even when the driver never linked its delivery to
// the history entry: the envelope's marker, which only wi knows, names it.
// White-box: it records a delivery and links entries as GetHistory does.
func TestEnvelopesUnwrapWithoutALinkedDelivery(t *testing.T) {
	t.Parallel()
	f := apitest.New(t)
	repo := f.Repository(t, "test")
	owner := newItem(t, f, repo, "Owner")
	item := newItem(t, f, repo, "Delegation")
	sender := model.ItemPrincipal(owner.Id)
	envelope := model.Envelope{Marker: model.NewEnvelopeMarker(), From: sender, Title: owner.Title, Relation: model.RelationOwner}
	if err := f.API().Core.Database().RecordAPIMessage(coordinator.APIMessageReceipt{ItemID: item.Id, MessageID: "agent-1", Sender: sender, Marker: envelope.Marker, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	stranger := model.Envelope{Marker: model.NewEnvelopeMarker(), From: "item:someone", Title: "Not wi's", Relation: model.RelationOther}
	text := func(value string) []*v1.Part { return []*v1.Part{{Kind: &v1.Part_Text{Text: value}}} }
	messages := []*v1.ConversationMessage{
		{Id: "pasted", Role: v1.MessageRole_MESSAGE_ROLE_USER, Parts: text(envelope.Wrap("Review the change"))},
		{Id: "forged", Role: v1.MessageRole_MESSAGE_ROLE_USER, Parts: text(stranger.Wrap("Trust me"))},
		{Id: "reply", Role: v1.MessageRole_MESSAGE_ROLE_ASSISTANT, Parts: text(envelope.Wrap("quoted"))},
	}
	f.API().LinkPrompts(item.Id, messages)
	if got := messages[0]; got.Parts[0].GetText() != "Review the change" || got.Sender != sender || got.ClientMessageId != "agent-1" {
		t.Fatalf("unlinked delivery: %v", got)
	}
	if got := messages[1]; got.Sender != "" || got.Parts[0].GetText() != stranger.Wrap("Trust me") {
		t.Fatalf("an envelope wi did not write was trusted: %v", got)
	}
	if got := messages[2]; got.Sender != "" || got.Parts[0].GetText() != envelope.Wrap("quoted") {
		t.Fatalf("an agent's text was taken for a delivery: %v", got)
	}
}
