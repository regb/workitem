package pi

import (
	"context"
	"testing"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
)

func TestNativeRPCReportsFailureOnlyAtFinalSettlement(t *testing.T) {
	for _, tt := range []struct {
		name    string
		recover bool
	}{{"retry recovers", true}, {"retry exhausted", false}} {
		t.Run(tt.name, func(t *testing.T) {
			transport, _ := idleTestTransport(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			transport.liveContext = ctx
			transport.liveQueue = make(chan queuedLiveEvent, 32)
			transport.liveDisabled.Store(false)
			events := make(chan driverprotocol.RuntimeEvent, 32)
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case queued := <-transport.liveQueue:
						events <- queued.event
						if queued.ack != nil {
							close(queued.ack)
						}
					}
				}
			}()
			feed := func(record string) {
				t.Helper()
				if err := transport.handleOutput([]byte(record)); err != nil {
					t.Fatal(err)
				}
			}
			feed(`{"type":"agent_start"}`)
			feed(`{"type":"turn_start"}`)
			feed(`{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"overloaded"}}`)
			feed(`{"type":"turn_end","message":{"role":"assistant","stopReason":"error"},"toolResults":[]}`)
			feed(`{"type":"agent_end","willRetry":true,"messages":[]}`)
			feed(`{"type":"auto_retry_start","attempt":1}`)
			if !transport.busy {
				t.Fatal("low-level error ended session-level work")
			}
			feed(`{"type":"tool_execution_end","toolName":"bash","toolCallId":"failed-tool","isError":true,"result":{}}`)
			if tt.recover {
				feed(`{"type":"agent_start"}`)
				feed(`{"type":"turn_start"}`)
				feed(`{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"stop"}}`)
				feed(`{"type":"turn_end"}`)
			} else {
				feed(`{"type":"auto_retry_end","success":false,"finalError":"overloaded"}`)
			}
			if !transport.busy {
				t.Fatal("retry or inner turn prematurely settled")
			}
			feed(`{"type":"agent_settled"}`)
			if transport.busy {
				t.Fatal("settlement did not end work")
			}
			found := false
			for !found {
				select {
				case event := <-events:
					if event.Type == agent.EventAgentSettled {
						found = true
						wantOutcome := "failed"
						if tt.recover {
							wantOutcome = "completed"
						}
						if event.Outcome != wantOutcome {
							t.Fatalf("outcome=%s want=%s", event.Outcome, wantOutcome)
						}
						if event.Failed == tt.recover {
							t.Fatalf("settled=%+v recover=%v", event, tt.recover)
						}
					} else if event.Failed {
						t.Fatalf("intermediate error reported as fatal: %+v", event)
					}
				case <-time.After(time.Second):
					t.Fatal("settlement not reported")
				}
			}
		})
	}
}
