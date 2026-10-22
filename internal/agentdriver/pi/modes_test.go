package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agentdriver"
)

func TestRuntimeModesOfferStructuredControlInBothModes(t *testing.T) {
	modes := RuntimeModes()
	if err := modes.Validate(); err != nil {
		t.Fatal(err)
	}
	tui, headless := modes.Modes[driverprotocol.ModeTUI], modes.Modes[driverprotocol.ModeHeadless]
	if tui.Control != driverprotocol.ControlStructured || !tui.Terminal || headless.Control != driverprotocol.ControlStructured || headless.Terminal {
		t.Fatalf("modes = %+v", modes.Modes)
	}
	if !modes.Settings["model"].OnResume || !modes.Settings["thinking"].OnResume {
		t.Fatalf("settings = %+v", modes.Settings)
	}
}

func TestRuntimeModesCrossTheDriverProcessWithoutLaunching(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	marker := filepath.Join(t.TempDir(), "backend-started")
	executable := filepath.Join(t.TempDir(), "fake-pi")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 9\n"), 0700); err != nil {
		t.Fatal(err)
	}
	client := agentdriver.Client{Driver: "pi", Path: os.Args[0], Args: []string{"-test.run=^TestDecodeTransportHelper$"}, Env: append(os.Environ(), "GO_WANT_WI_PI_DECODE_HELPER=1")}
	raw, _ := json.Marshal(Config{Executable: executable})
	modes, err := client.RuntimeModes(context.Background(), "default", raw)
	if err != nil {
		t.Fatal(err)
	}
	if modes.Modes[driverprotocol.ModeTUI].Control != driverprotocol.ControlStructured || !modes.Modes[driverprotocol.ModeTUI].Terminal {
		t.Fatalf("modes = %+v", modes)
	}
	if _, err := client.RuntimeModes(context.Background(), "default", json.RawMessage(`{"unknown":true}`)); err == nil {
		t.Fatal("invalid configuration accepted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("runtime.modes launched the backend")
	}
}

func TestLiveSettingsMapToPiRPCCommands(t *testing.T) {
	model, err := nativeSetting(driverprotocol.Setting{Name: "model", Value: "openai-codex/gpt-6-sol"})
	if err != nil || model["type"] != "set_model" || model["provider"] != "openai-codex" || model["modelId"] != "gpt-6-sol" {
		t.Fatalf("model=%v err=%v", model, err)
	}
	thinking, err := nativeSetting(driverprotocol.Setting{Name: "thinking", Value: "high"})
	if err != nil || thinking["type"] != "set_thinking_level" || thinking["level"] != "high" {
		t.Fatalf("thinking=%v err=%v", thinking, err)
	}
	for _, bad := range []driverprotocol.Setting{{Name: "model", Value: "no-provider"}, {Name: "model", Value: "/x"}, {Name: "thinking", Value: "extreme"}, {Name: "executable", Value: "x"}} {
		if _, err := nativeSetting(bad); err == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
}

func TestModelListParsesPiTable(t *testing.T) {
	catalog := parseModelList("provider      model                context  max-out  thinking  images\nopenai-codex  gpt-5.3-codex-spark  128K     128K     yes       no\nlocal         tiny                 8K       4K       no        no\n\n")
	if catalog.ModelSetting != "model" || catalog.ReasoningSetting != "thinking" || len(catalog.Models) != 2 {
		t.Fatalf("catalog = %+v", catalog)
	}
	if first := catalog.Models[0]; first.Value != "openai-codex/gpt-5.3-codex-spark" || !first.Reasoning || first.ImageInput {
		t.Fatalf("first = %+v", first)
	}
	if catalog.Models[1].Reasoning {
		t.Fatal("non-thinking model marked as reasoning")
	}
}
