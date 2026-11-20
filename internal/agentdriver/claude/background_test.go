package claude

import (
	"reflect"
	"testing"
)

func TestBackgroundWorkIsReadFromTheTranscript(t *testing.T) {
	for _, c := range []struct {
		name           string
		record         string
		started, ended []string
	}{
		{"background command", `{"type":"user","timestamp":"2026-11-18T20:04:03Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"Command running in background with ID: ba5n40m21."}]},"toolUseResult":{"stdout":"","backgroundTaskId":"ba5n40m21"}}`, []string{"ba5n40m21"}, nil},
		{"async subagent", `{"type":"user","timestamp":"2026-11-18T20:03:39Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"launched"}]},"toolUseResult":{"isAsync":true,"status":"async_launched","agentId":"a2d13d6a8ecb2d76c"}}`, []string{"a2d13d6a8ecb2d76c"}, nil},
		{"monitor", `{"type":"user","timestamp":"2026-11-18T16:29:48Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"Monitor started"}]},"toolUseResult":{"taskId":"bat9hane4","timeoutMs":1800000,"persistent":false}}`, []string{"bat9hane4"}, nil},
		{"stopped task", `{"type":"user","timestamp":"2026-11-18T16:30:27Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"stopped"}]},"toolUseResult":{"message":"Successfully stopped task: bat9hane4","task_id":"bat9hane4","task_type":"local_bash"}}`, nil, []string{"bat9hane4"}},
		{"completed task", `{"type":"user","timestamp":"2026-11-18T20:04:20Z","message":{"role":"user","content":"<task-notification>\n<task-id>ba5n40m21</task-id>\n<tool-use-id>t</tool-use-id>\n<status>completed</status>\n<summary>done</summary>\n</task-notification>"}}`, nil, []string{"ba5n40m21"}},
		{"killed task", `{"type":"user","timestamp":"2026-11-18T19:54:38Z","message":{"role":"user","content":[{"type":"text","text":"<task-notification><task-id>b01nw8bli</task-id><status>killed</status></task-notification>"}]}}`, nil, []string{"b01nw8bli"}},
		{"monitor event keeps it running", `{"type":"user","timestamp":"2026-11-18T16:31:36Z","message":{"role":"user","content":"<task-notification>\n<task-id>bhuyi5vdq</task-id>\n<summary>Monitor event</summary>\n<event>MISMATCH</event>\n</task-notification>"}}`, nil, nil},
		{"monitor expiry ends it", `{"type":"user","timestamp":"2026-11-18T17:01:50Z","message":{"role":"user","content":"<task-notification>\n<task-id>b1kdxwv8s</task-id>\n<event>[Monitor expired after 30m with 1 event delivered.]</event>\n</task-notification>"}}`, nil, []string{"b1kdxwv8s"}},
		{"task notification queued into a running turn", `{"type":"attachment","timestamp":"2026-11-20T08:49:31Z","attachment":{"type":"queued_command","prompt":"<task-notification>\n<task-id>b0vn8fq20</task-id>\n<tool-use-id>t</tool-use-id>\n<status>completed</status>\n<summary>done</summary>\n</task-notification>","commandMode":"task-notification"}}`, nil, []string{"b0vn8fq20"}},
		{"operator message queued into a running turn", `{"type":"attachment","timestamp":"2026-11-20T08:49:31Z","attachment":{"type":"queued_command","prompt":"also check the docs","commandMode":"prompt"}}`, nil, nil},
		{"ordinary tool result", `{"type":"user","timestamp":"2026-11-18T20:04:22Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]},"toolUseResult":{"stdout":"ok","stderr":""}}`, nil, nil},
	} {
		fact, ok := summarize(1, []byte(c.record))
		if !ok || !reflect.DeepEqual(fact.BackgroundStarted, c.started) || !reflect.DeepEqual(fact.BackgroundEnded, c.ended) {
			t.Errorf("%s: ok=%v started=%v ended=%v", c.name, ok, fact.BackgroundStarted, fact.BackgroundEnded)
		}
	}
}
