package daemonapi

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

// An agent's answer goes back to whoever asked. When an item's agent sends
// another a message, the turn that answers it ends with a notice to the
// sender: the reply, or the question the agent waits on. A delegation's
// owner hears of the turns it asked for, its first prompt included, as any
// item would. A person's own messages are answered where they asked, with no
// notice. The notice is a message from the answering item, so it arrives in
// the usual envelope; it waits for the asker's current turn rather than
// interrupting it, and starts its agent when it is not running. No notice is
// sent for a turn the asker waits on with wi send --wait, the reply being
// that command's result, for a turn that answered a notice, which would
// bounce answers between agents, nor to a closed item.

// noticeClient is the control client notices are sent under when no client
// holds the owning item's control.
const noticeClient = "wi-notice"

// noticeExcerpt bounds how much of a reply a notice quotes.
const noticeExcerpt = 1500

// questionReasons are the runtime reasons that mean the agent waits on a
// question someone may answer.
var questionReasons = map[string]bool{"question_pending": true, "plan_approval_pending": true}

// noticed remembers, per item, that its current question was announced, so
// repeated status reports announce it once.
type noticed struct {
	mu        sync.Mutex
	questions map[string]bool
}

func (n *noticed) question(itemID string, asking bool) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.questions == nil {
		n.questions = map[string]bool{}
	}
	if !asking {
		delete(n.questions, itemID)
		return false
	}
	if n.questions[itemID] {
		return false
	}
	n.questions[itemID] = true
	return true
}

// observeRuntimeEvent turns a delegation's settled turn or new question into
// a notice to its owner. It schedules the work: the coordinator calls it
// while recording the event.
func (s *Server) observeRuntimeEvent(event coordinator.RuntimeSemanticEvent) {
	kind := ""
	switch {
	case event.Type == "agent.settled":
		s.notices.question(event.WorkItemID, false)
		kind = "settled"
		if event.Failed {
			kind = "failed"
		}
	case questionReasons[event.ReasonCode]:
		if !s.notices.question(event.WorkItemID, true) {
			return
		}
		kind = "question"
	default:
		return
	}
	if !s.startCallback() {
		return
	}
	go func() {
		defer s.callbacks.Done()
		ctx, cancel := context.WithTimeout(s.ctx, time.Minute)
		defer cancel()
		_ = s.noticeOwner(ctx, event, kind)
	}()
}

// noticeOwner returns the answer of an agent's turn to whoever asked for
// it: the item whose message started it, its owner or another, gets a
// notice of the reply or question. A turn a person asked for, from the web,
// the command line or the agent's terminal, is answered to them where they
// asked: no item is noticed. Nor is one for a turn that answered a notice,
// which would bounce answers between agents.
func (s *Server) noticeOwner(ctx context.Context, event coordinator.RuntimeSemanticEvent, kind string) error {
	current, err := s.canonical(ctx, event.WorkItemID, 0)
	if err != nil {
		return err
	}
	answering := current.Manifest
	askerID := s.turnAsker(ctx, answering.ID)
	if askerID == "" || askerID == answering.ID {
		return nil
	}
	// A closed item gets no notice; an owner that closed gave its
	// delegations back. Any other gets it, its agent started if it is not
	// running, as a message to it would.
	asker, err := s.canonical(ctx, askerID, 0)
	if err != nil || asker.Manifest.State == model.StateClosed {
		return err
	}
	if kind == "settled" && !answering.Delegated() {
		// The turn answered an item's agent, not the person whose item this
		// is: it does not wait for them as unread. Its conversation still
		// shows it, named as another item's agent's.
		_, _ = s.App.MarkRead(ctx, resolve(answering.ID))
	}
	if kind == "settled" {
		// The asker waits on this very turn: the reply is its result.
		if latest, found, err := s.Core.Database().LatestAPIMessage(answering.ID); err == nil && found && latest.AwaitsReply && latest.Sender == model.ItemPrincipal(askerID) {
			return nil
		}
	}
	text := s.noticeText(ctx, answering, askerID, kind)
	origin := model.Origin{Actor: model.ItemPrincipal(answering.ID), Cause: "notice:" + kind}
	ctx, done := s.actOnItself(ctx, askerID, noticeClient, "notice", origin)
	delivery := v1.Delivery_DELIVERY_PROMPT
	if s.App.AgentBusy(ctx, askerID) {
		delivery = v1.Delivery_DELIVERY_FOLLOW_UP
	}
	unlock := s.lock(askerID)
	if running, _ := s.App.AgentRuntimeStatus(ctx, resolve(askerID)); !running.Online {
		delivery = v1.Delivery_DELIVERY_UNSPECIFIED
	}
	sent, err := s.SendMessage(ctx, connect.NewRequest(&v1.SendMessageRequest{ItemId: askerID, Text: text, Delivery: delivery, ClientMessageId: noticePrefix + event.ID, StartMode: v1.AgentMode_AGENT_MODE_HEADLESS}))
	unlock()
	var job *v1.Job
	if err == nil {
		job = sent.Msg.Job
	}
	done(job)
	return err
}

// noticePrefix begins a notice's message ID.
const noticePrefix = "notice-"

// turnAsker is the item whose message the agent's latest turn answers: the
// sender of the last prompt in its conversation, if an item's agent sent it
// and it was not a notice. Completions of background work are not prompts:
// they continue the turn that started the work.
func (s *Server) turnAsker(ctx context.Context, itemID string) string {
	history, err := s.GetHistory(ctx, connect.NewRequest(&v1.GetHistoryRequest{ItemId: itemID}))
	if err != nil {
		return ""
	}
	messages := history.Msg.Messages
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != v1.MessageRole_MESSAGE_ROLE_USER {
			continue
		}
		var text strings.Builder
		for _, part := range m.Parts {
			text.WriteString(part.GetText())
		}
		if strings.HasPrefix(strings.TrimSpace(text.String()), "<task-notification>") {
			continue
		}
		if strings.HasPrefix(m.ClientMessageId, noticePrefix) {
			return ""
		}
		return model.PrincipalItem(m.Sender)
	}
	return ""
}

// noticeText says what the delegation did, quoting its reply or question.
func (s *Server) noticeText(ctx context.Context, delegation model.Manifest, askerID, kind string) string {
	name := fmt.Sprintf("%q (%s)", delegation.Title, delegation.ID)
	if delegation.Owner == model.ItemPrincipal(askerID) {
		name = "your delegation " + name
	} else {
		name = "the item you messaged, " + name + ","
	}
	var b strings.Builder
	switch kind {
	case "question":
		reason := s.observation(ctx, delegation).GetAgentReason()
		fmt.Fprintf(&b, "wi notice: %s is waiting on a question", name)
		if reason != "" {
			fmt.Fprintf(&b, ": %s", reason)
		}
		fmt.Fprintf(&b, ".\nIts conversation shows the question; answer it with wi send %s, or ask your user.", delegation.ID)
	case "failed":
		reason := s.observation(ctx, delegation).GetAgentReason()
		fmt.Fprintf(&b, "wi notice: %s stopped with a problem", name)
		if reason != "" {
			fmt.Fprintf(&b, ": %s", reason)
		}
		fmt.Fprintf(&b, ".\nSee wi show %s; tell your user if you cannot resolve it.", delegation.ID)
	default:
		fmt.Fprintf(&b, "wi notice: %s finished its turn.", name)
		if reply := s.lastReply(ctx, delegation.ID); reply != "" {
			fmt.Fprintf(&b, " Its last reply:\n\n%s", reply)
		} else {
			b.WriteString(" It did not reply.")
		}
		if delegation.Owner == model.ItemPrincipal(askerID) {
			fmt.Fprintf(&b, "\n\nAnswer it with wi send %s. Once its work is done, merge it and close it yourself.", delegation.ID)
		} else {
			fmt.Fprintf(&b, "\n\nAnswer it with wi send %s if you need more; its next reply comes back the same way.", delegation.ID)
		}
	}
	return b.String()
}

// lastReply is the text of the agent's last reply, shortened.
func (s *Server) lastReply(ctx context.Context, itemID string) string {
	history, err := s.GetHistory(ctx, connect.NewRequest(&v1.GetHistoryRequest{ItemId: itemID}))
	if err != nil {
		return ""
	}
	messages := history.Msg.Messages
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != v1.MessageRole_MESSAGE_ROLE_ASSISTANT {
			continue
		}
		var parts []string
		for _, part := range messages[i].Parts {
			if text := strings.TrimSpace(part.GetText()); text != "" {
				parts = append(parts, text)
			}
		}
		if text := strings.Join(parts, "\n"); text != "" {
			if len(text) > noticeExcerpt {
				cut := noticeExcerpt
				for cut > 0 && !utf8RuneStart(text[cut]) {
					cut--
				}
				text = text[:cut] + "…"
			}
			return text
		}
	}
	return ""
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }
