package daemonapi_test

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
)

// An agent's answer goes back to whoever asked: an item that messages
// another, not its delegation, hears of the turn that answers it, and its
// own turn answering that notice is not bounced back.
func TestAnswersGoBackToTheItemThatAsked(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.WithAgents())
	repo := f.Repository(t, "test")
	asker, _ := activeItem(t, f, repo, "Asker")
	startAgent(t, f, asker.Id)
	answerer, _ := activeItem(t, f, repo, "Answerer")
	startAgent(t, f, answerer.Id)
	userMessages := func(item string) []string {
		var out []string
		for _, m := range call(t, f.Clients.Agents.GetHistory, &v1.GetHistoryRequest{ItemId: item}).Messages {
			if m.Role == v1.MessageRole_MESSAGE_ROLE_USER {
				out = append(out, messageText(m))
			}
		}
		return out
	}
	accessSend(t, accessRuntimeClients(t, f, asker.Id), &v1.SendMessageRequest{ItemId: answerer.Id, Text: "what do you think"})
	eventually(t, "the asker's notice", func() bool {
		for _, text := range userMessages(asker.Id) {
			if strings.HasPrefix(text, "wi notice: the item you messaged") && strings.Contains(text, "what do you think") {
				return true
			}
		}
		return false
	})
	apitest.WaitIdle(t, f.Clients, asker.Id)
	time.Sleep(time.Second)
	for _, text := range userMessages(answerer.Id) {
		if strings.HasPrefix(text, "wi notice:") {
			t.Fatalf("the answer to a notice bounced back: %q", text)
		}
	}
}
