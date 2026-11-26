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

// A delegated item's attention goes to the item that owns it, as a person's
// items' attention goes to them: when the delegation's turn ends, or it waits
// on a question, the daemon sends the owning item's agent a notice. The
// notice is a message from the delegation, so it arrives in the usual
// envelope; it waits for the owner's current turn rather than interrupting
// it. No notice is sent for a turn the owner is already waiting on with
// wi send --wait: the reply is that command's result.

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

// noticeOwner sends the owning item's agent a notice of what its delegation
// did, when it has a running agent to read it.
func (s *Server) noticeOwner(ctx context.Context, event coordinator.RuntimeSemanticEvent, kind string) error {
	current, err := s.canonical(ctx, event.WorkItemID, 0)
	if err != nil {
		return err
	}
	delegation := current.Manifest
	ownerID := model.PrincipalItem(delegation.Owner)
	if ownerID == "" {
		return nil
	}
	owner, err := s.canonical(ctx, ownerID, 0)
	if err != nil || owner.Manifest.State != model.StateActive {
		return err
	}
	runtime, err := s.App.AgentRuntimeStatus(ctx, resolve(ownerID))
	if err != nil || !runtime.Online {
		return err
	}
	if kind == "settled" {
		// The owner is waiting on this very turn: the reply is its result.
		if latest, found, err := s.Core.Database().LatestAPIMessage(delegation.ID); err == nil && found && latest.AwaitsReply && latest.Sender == model.ItemPrincipal(ownerID) {
			return nil
		}
	}
	text := s.noticeText(ctx, delegation, kind)
	origin := model.Origin{Actor: model.ItemPrincipal(delegation.ID), Cause: "notice:" + kind}
	ctx, done := s.actOnItself(ctx, ownerID, noticeClient, "notice", origin)
	delivery := v1.Delivery_DELIVERY_PROMPT
	if s.App.AgentBusy(ctx, ownerID) {
		delivery = v1.Delivery_DELIVERY_FOLLOW_UP
	}
	unlock := s.lock(ownerID)
	sent, err := s.SendMessage(ctx, connect.NewRequest(&v1.SendMessageRequest{ItemId: ownerID, Text: text, Delivery: delivery, RequireOnline: true, ClientMessageId: "notice-" + event.ID}))
	unlock()
	var job *v1.Job
	if err == nil {
		job = sent.Msg.Job
	}
	done(job)
	return err
}

// noticeText says what the delegation did, quoting its reply or question.
func (s *Server) noticeText(ctx context.Context, delegation model.Manifest, kind string) string {
	name := fmt.Sprintf("%q (%s)", delegation.Title, delegation.ID)
	var b strings.Builder
	switch kind {
	case "question":
		reason := s.observation(ctx, delegation).GetAgentReason()
		fmt.Fprintf(&b, "wi notice: your delegation %s is waiting on a question", name)
		if reason != "" {
			fmt.Fprintf(&b, ": %s", reason)
		}
		fmt.Fprintf(&b, ".\nIts conversation shows the question; answer it with wi send %s, or ask your user.", delegation.ID)
	case "failed":
		reason := s.observation(ctx, delegation).GetAgentReason()
		fmt.Fprintf(&b, "wi notice: your delegation %s stopped with a problem", name)
		if reason != "" {
			fmt.Fprintf(&b, ": %s", reason)
		}
		fmt.Fprintf(&b, ".\nSee wi show %s; tell your user if you cannot resolve it.", delegation.ID)
	default:
		fmt.Fprintf(&b, "wi notice: your delegation %s finished its turn.", name)
		if reply := s.lastReply(ctx, delegation.ID); reply != "" {
			fmt.Fprintf(&b, " Its last reply:\n\n%s", reply)
		} else {
			b.WriteString(" It did not reply.")
		}
		fmt.Fprintf(&b, "\n\nAnswer it with wi send %s, or merge or close it when its work is done.", delegation.ID)
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
