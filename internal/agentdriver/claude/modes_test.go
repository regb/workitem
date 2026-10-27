package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

func TestRuntimeModesReportKeystrokeTUIAndStructuredHeadless(t *testing.T) {
	modes := RuntimeModes()
	if err := modes.Validate(); err != nil {
		t.Fatal(err)
	}
	if tui := modes.Modes[protocol.ModeTUI]; tui.Control != protocol.ControlEmulated || !tui.Terminal || tui.IdleGuard {
		t.Fatalf("tui = %+v", tui)
	}
	if headless := modes.Modes[protocol.ModeHeadless]; headless.Control != protocol.ControlStructured || headless.Terminal {
		t.Fatalf("headless = %+v", headless)
	}
	if !modes.Settings["model"].OnResume || !modes.Settings["reasoning"].OnResume {
		t.Fatalf("settings = %+v", modes.Settings)
	}
}

func TestModelCatalogFromInitializeOmitsDefault(t *testing.T) {
	catalog, err := parseModelCatalog([]byte(`{"models":[{"value":"default","displayName":"Default"},{"value":"opus","displayName":"Opus 5.5","description":"complex work","supportsEffort":true,"supportedEffortLevels":["low","high"]},{"value":"claude-haiku-4-5","displayName":"Haiku 4.5"}]}`))
	if err != nil || len(catalog.ReasoningLevels) != 5 || len(catalog.Models) != 2 {
		t.Fatalf("catalog=%+v err=%v", catalog, err)
	}
	if opus := catalog.Models[0]; opus.Value != "opus" || !opus.Reasoning || len(opus.Levels) != 2 {
		t.Fatalf("opus = %+v", opus)
	}
	if catalog.Models[1].Reasoning {
		t.Fatal("model without effort marked as reasoning")
	}
	if _, err := parseModelCatalog([]byte(`{"models":[]}`)); err == nil {
		t.Fatal("empty catalog accepted")
	}
}

func TestModelCatalogNamesTheDefaultAndAliasModels(t *testing.T) {
	catalog, err := parseModelCatalog([]byte(`{"models":[{"value":"default","resolvedModel":"claude-opus-5-5"},{"value":"opus","resolvedModel":"claude-opus-5-5"},{"value":"claude-opus-4-8","resolvedModel":"claude-opus-4-8"}]}`))
	if err != nil || catalog.Default != "claude-opus-5-5" {
		t.Fatalf("catalog=%+v err=%v", catalog, err)
	}
	if catalog.Models[0].Resolved != "claude-opus-5-5" || catalog.Models[1].Resolved != "" {
		t.Fatalf("models = %+v", catalog.Models)
	}
}

func TestSettingsReportResolvesAliasesAndSkipsRepeats(t *testing.T) {
	var events []protocol.RuntimeEvent
	report := &settingsReport{emit: func(event protocol.RuntimeEvent) { events = append(events, event) }, aliases: map[string]string{"opus": "claude-opus-5-5"}}
	report.report(protocol.SettingModel, "opus")
	report.report(protocol.SettingModel, "claude-opus-5-5")
	report.report(protocol.SettingModel, "<synthetic>")
	report.report(protocol.SettingReasoning, "")
	report.report(protocol.SettingReasoning, "extreme")
	report.report(protocol.SettingReasoning, "high")
	if len(events) != 2 || events[0].Type != protocol.EventSettingsChanged || events[0].Settings["model"] != "claude-opus-5-5" || events[1].Settings["reasoning"] != "high" {
		t.Fatalf("events = %+v", events)
	}
}

func TestHeadlessSettingsUseControlRequestsAndSettleOnResponse(t *testing.T) {
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	frames := make(chan map[string]any, 4)
	go func() {
		d := json.NewDecoder(r)
		for {
			var f map[string]any
			if d.Decode(&f) != nil {
				return
			}
			frames <- f
		}
	}()
	events := []protocol.RuntimeEvent{}
	s := &headlessControls{input: w, initialized: true, emit: func(e protocol.RuntimeEvent) { events = append(events, e) }}
	if err := s.accept(protocol.ControlCommand{ID: "m", Type: protocol.CommandSet, Setting: &protocol.Setting{Name: "model", Value: "sonnet"}}); err != nil {
		t.Fatal(err)
	}
	model := <-frames
	request := model["request"].(map[string]any)
	if request["subtype"] != "set_model" || request["model"] != "sonnet" {
		t.Fatalf("model request = %v", model)
	}
	if err := s.accept(protocol.ControlCommand{ID: "e", Type: protocol.CommandSet, Setting: &protocol.Setting{Name: "reasoning", Value: "xhigh"}}); err != nil {
		t.Fatal(err)
	}
	effort := <-frames
	if settings := effort["request"].(map[string]any)["settings"].(map[string]any); effort["request"].(map[string]any)["subtype"] != "apply_flag_settings" || settings["effortLevel"] != "xhigh" {
		t.Fatalf("effort request = %v", effort)
	}
	accepted := headlessFrame{Type: "control_response"}
	accepted.Response.RequestID, accepted.Response.Subtype = model["request_id"].(string), "success"
	s.settingResponse(accepted)
	rejected := headlessFrame{Type: "control_response"}
	rejected.Response.RequestID, rejected.Response.Subtype, rejected.Response.Error = effort["request_id"].(string), "error", "nope"
	s.settingResponse(rejected)
	if len(events) != 2 || events[0].Type != protocol.EventCommandAccepted || events[0].CommandID != "m" || events[1].Type != protocol.EventCommandRejected || events[1].CommandID != "e" {
		t.Fatalf("events = %+v", events)
	}
	// The daemon validates delivery states; a refused setting must use one it accepts.
	for _, event := range events {
		if !protocol.ValidDeliveryState(event.DeliveryState) || !protocol.ValidReasonCode(event.ReasonCode) {
			t.Fatalf("event the daemon would refuse: %+v", event)
		}
	}
	for _, bad := range []protocol.Setting{{Name: "reasoning", Value: "extreme"}, {Name: "executable", Value: "x"}} {
		if s.accept(protocol.ControlCommand{ID: "bad-" + bad.Name, Type: protocol.CommandSet, Setting: &bad}) == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
}

func TestTranscriptRepliesReportModelAndEffort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	start := time.Now().UTC()
	old := start.Add(-time.Hour).Format(time.RFC3339Nano)
	recent := start.Add(time.Second).Format(time.RFC3339Nano)
	records := `{"type":"assistant","uuid":"a","timestamp":"` + old + `","effort":"low","message":{"model":"claude-old","content":[{"type":"text","text":"before"}]}}
{"type":"assistant","uuid":"b","timestamp":"` + recent + `","effort":"xhigh","message":{"model":"claude-opus-5-5","content":[{"type":"text","text":"now"}]}}
`
	if err := os.WriteFile(path, []byte(records), 0600); err != nil {
		t.Fatal(err)
	}
	var events []protocol.RuntimeEvent
	report := &settingsReport{emit: func(event protocol.RuntimeEvent) { events = append(events, event) }}
	feed := &transcriptFeed{factAfter: start}
	if _, err := feed.refresh(path, ignoredTranscript{}); err != nil {
		t.Fatal(err)
	}
	report.replies(feed)
	if len(events) != 2 || events[0].Settings["model"] != "claude-opus-5-5" || events[1].Settings["reasoning"] != "xhigh" {
		t.Fatalf("events = %+v", events)
	}
}
