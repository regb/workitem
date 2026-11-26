package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"

	"github.com/regb/workitem/internal/testutil"
)

const askedInput = `{"questions":[{"question":"Which color?","header":"Color","multiSelect":false,"options":[{"label":"Red"},{"label":"Blue"}]}]}`

func answerCommand(tool string, answers map[string]string) protocol.ControlCommand {
	return protocol.ControlCommand{ID: "answer-1", Type: protocol.CommandAnswer, ToolCallID: tool, Answers: answers}
}

func TestHeadlessHoldsAQuestionUntilItIsAnswered(t *testing.T) {
	writer, frames := pipeFrames[json.RawMessage](t)
	s := headlessControls{input: writer, initialized: true, emit: func(protocol.RuntimeEvent) {}}
	var frame headlessFrame
	if err := json.Unmarshal([]byte(`{"type":"control_request","request_id":"req-1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","tool_use_id":"toolu_1","input":`+askedInput+`}}`), &frame); err != nil {
		t.Fatal(err)
	}
	if !s.ask(frame) || s.question == nil {
		t.Fatal("the question was not held")
	}
	var other headlessFrame
	_ = json.Unmarshal([]byte(`{"type":"control_request","request_id":"req-2","request":{"subtype":"can_use_tool","tool_name":"ExitPlanMode","input":{}}}`), &other)
	if s.ask(other) {
		t.Fatal("another tool was held")
	}
	for _, c := range []protocol.ControlCommand{
		answerCommand("toolu_2", map[string]string{"Which color?": "Blue"}),
		answerCommand("toolu_1", map[string]string{"Which shape?": "Round"}),
	} {
		if err := s.accept(c); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	if err := s.accept(answerCommand("toolu_1", map[string]string{"Which color?": "Blue"})); err != nil {
		t.Fatal(err)
	}
	if s.question != nil {
		t.Fatal("the answered question is still held")
	}
	// Repeating the answer succeeds without writing again.
	if err := s.accept(answerCommand("toolu_1", map[string]string{"Which color?": "Blue"})); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	var data []json.RawMessage
	for frame := range frames {
		data = append(data, frame)
	}
	if len(data) != 1 {
		t.Fatalf("wrote %d frames for one answer", len(data))
	}
	var written struct {
		Response struct {
			RequestID string `json:"request_id"`
			Response  struct {
				Behavior     string         `json:"behavior"`
				UpdatedInput map[string]any `json:"updatedInput"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data[0], &written); err != nil {
		t.Fatalf("%v: %s", err, data[0])
	}
	answers, _ := written.Response.Response.UpdatedInput["answers"].(map[string]any)
	if written.Response.RequestID != "req-1" || written.Response.Response.Behavior != "allow" || answers["Which color?"] != "Blue" || written.Response.Response.UpdatedInput["questions"] == nil {
		t.Fatalf("written: %s", data[0])
	}
}

func TestTUIQuestionHookWaitsForTheRuntimesAnswer(t *testing.T) {
	path := filepath.Join(testutil.ShortTempDir(t), "hooks.sock")
	server, err := protocol.ListenControl(path)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	var input askInput
	if err := json.Unmarshal([]byte(`{"session_id":"s","tool_use_id":"toolu_1","tool_input":`+askedInput+`}`), &input); err != nil {
		t.Fatal(err)
	}
	hook := func() chan string {
		out := make(chan string, 1)
		go func() {
			data, _ := json.Marshal(input)
			var output bytes.Buffer
			RunAskHook(context.Background(), path, bytes.NewReader(data), &output)
			out <- output.String()
		}()
		return out
	}
	controls := &tuiControls{}
	receive := func() protocol.ControlRequest {
		select {
		case request := <-server.Requests():
			return request
		case <-time.After(5 * time.Second):
			t.Fatal("the hook did not ask")
		}
		return protocol.ControlRequest{}
	}

	// Answered through wi: the hook allows the call with the answers.
	out := hook()
	request := receive()
	var asked askInput
	if err := json.Unmarshal([]byte(request.Command.Message), &asked); err != nil || request.Command.Type != "ask" || asked.ToolUseID != "toolu_1" {
		t.Fatalf("ask: %+v %v", request.Command, err)
	}
	controls.hold(request, asked)
	if err := controls.answer(answerCommand("toolu_1", map[string]string{"Which shape?": "Round"})); err == nil {
		t.Fatal("an answer to another question was accepted")
	}
	if err := controls.answer(answerCommand("toolu_1", map[string]string{"Which color?": "Blue"})); err != nil {
		t.Fatal(err)
	}
	var decided struct {
		Specific struct {
			Event        string         `json:"hookEventName"`
			Decision     string         `json:"permissionDecision"`
			UpdatedInput map[string]any `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(<-out), &decided); err != nil {
		t.Fatal(err)
	}
	answers, _ := decided.Specific.UpdatedInput["answers"].(map[string]any)
	if decided.Specific.Event != "PreToolUse" || decided.Specific.Decision != "allow" || answers["Which color?"] != "Blue" || decided.Specific.UpdatedInput["questions"] == nil {
		t.Fatalf("hook output: %+v", decided)
	}
	if err := controls.answer(answerCommand("toolu_1", map[string]string{"Which color?": "Blue"})); err != nil {
		t.Fatalf("repeating the answer: %v", err)
	}

	// Released, as when the terminal attaches: Claude shows its dialog.
	out = hook()
	controls.hold(receive(), asked)
	controls.release()
	if got := <-out; strings.TrimSpace(got) != "" {
		t.Fatalf("a released hook decided: %q", got)
	}
	controls.asking = "question_pending"
	if err := controls.answer(answerCommand("", map[string]string{"Which color?": "Red"})); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("answering a dialog shown in the terminal: %v", err)
	}

	// Without its runtime, the hook leaves the question to Claude's dialog.
	server.Close()
	if got := <-hook(); got != "" {
		t.Fatalf("hook without a runtime decided: %q", got)
	}
}
