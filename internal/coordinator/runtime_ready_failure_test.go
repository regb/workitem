package coordinator

import (
	"github.com/regb/workitem/internal/agent"
	"testing"
	"time"
)

func TestLateReadinessDoesNotEraseFailedTurn(t *testing.T) {
	observation := AgentObservation{}
	activity := RuntimeActivity{}
	apply := func(observation *AgentObservation, event RuntimeSemanticEvent) {
		applyRuntimeActivity(&activity, runtimeEventHeader{Type: event.Type, Timestamp: event.Timestamp, Failed: event.Failed, Readiness: event.Readiness}, "", "")
		applyLiveRuntimeObservation(observation, event, activity)
	}
	apply(&observation, RuntimeSemanticEvent{Type: agent.EventAgentSettled, Failed: true, Timestamp: time.Now()})
	for _, kind := range []string{agent.EventRuntimeReady, agent.EventRuntimeStatus} {
		apply(&observation, RuntimeSemanticEvent{Type: kind, Readiness: "ready", ReasonCode: "headless_ready", Timestamp: time.Now()})
		if observation.Status != "problem" || observation.TurnState != "failed" {
			t.Fatal("input readiness erased failed turn", observation)
		}
	}
	apply(&observation, RuntimeSemanticEvent{Type: agent.EventAgentStarted, Timestamp: time.Now()})
	if observation.Status != "busy" {
		t.Fatal(observation)
	}
	for _, kind := range []string{agent.EventRuntimeReady, agent.EventRuntimeStatus} {
		apply(&observation, RuntimeSemanticEvent{Type: agent.EventAgentSettled, Failed: true, Timestamp: time.Now()})
		apply(&observation, RuntimeSemanticEvent{Type: kind, Readiness: "busy", ReasonCode: "active_turn", Timestamp: time.Now()})
		if observation.Status != "busy" || observation.TurnState != "incomplete" {
			t.Fatal("fresh work failed to reactivate observation", observation)
		}
	}
}
