package coordinator

import (
	"testing"
	"time"
)

func TestFailedSettledEventIsActionable(t *testing.T) {
	for _, kind := range []string{"agent.settled", "turn.ended"} {
		observation := AgentObservation{Status: "busy"}
		activity := RuntimeActivity{}
		event := RuntimeSemanticEvent{Type: kind, Failed: true, Timestamp: time.Now()}
		applyRuntimeActivity(&activity, runtimeEventHeader{Type: kind, Failed: true, Timestamp: event.Timestamp}, "", "")
		applyLiveRuntimeObservation(&observation, event, activity)
		if observation.Status != "problem" || observation.TurnState != "failed" {
			t.Fatalf("%+v", observation)
		}
		event = RuntimeSemanticEvent{Type: "agent.started", Timestamp: time.Now()}
		applyRuntimeActivity(&activity, runtimeEventHeader{Type: event.Type, Timestamp: event.Timestamp}, "", "")
		applyLiveRuntimeObservation(&observation, event, activity)
		if observation.Status != "busy" {
			t.Fatalf("%+v", observation)
		}
	}
}
func TestClaudeToolResultAndInterruptionIndex(t *testing.T) {
	index := ConversationIndex{}
	attention := AttentionActivity{}
	at := time.Now()
	applyConversationFact(&index, &attention, ConversationEventFact{Role: "user", Timestamp: at, ContentTypes: []string{"text"}})
	applyConversationFact(&index, &attention, ConversationEventFact{Role: "assistant", Timestamp: at.Add(time.Second), ContentTypes: []string{"tool_use"}})
	applyConversationFact(&index, &attention, ConversationEventFact{Role: "user", Timestamp: at.Add(2 * time.Second), ContentTypes: []string{"tool_result"}})
	if index.LastUserPrompt.Timestamp != at || index.LastToolActivity == nil || !index.LastToolActivity.Timestamp.Equal(at.Add(2*time.Second)) {
		t.Fatalf("%+v", index)
	}
	applyConversationFact(&index, &attention, ConversationEventFact{Role: "user", Timestamp: at.Add(3 * time.Second), Terminal: true, Failed: true, StopReason: "interrupted"})
	if inferIndexedConversationTurnState(index) != "failed" {
		t.Fatalf("%+v", index)
	}
}
