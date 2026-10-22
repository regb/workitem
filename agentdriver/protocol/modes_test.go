package driverprotocol

import "testing"

func TestRuntimeModesValidateAndChoose(t *testing.T) {
	modes := RuntimeModes{Modes: map[Mode]ModeCapabilities{
		ModeTUI:      {Control: ControlEmulated, Terminal: true},
		ModeHeadless: {Control: ControlStructured},
	}, Settings: map[string]SettingChange{"model": {OnResume: true, Live: []Mode{ModeHeadless}}}}
	if err := modes.Validate(); err != nil {
		t.Fatal(err)
	}
	structured := func(c ModeCapabilities) bool { return c.Control == ControlStructured }
	if got := modes.ModesWith(structured, ModeTUI); len(got) != 1 || got[0] != ModeHeadless {
		t.Fatalf("structured modes = %v", got)
	}
	terminal := func(c ModeCapabilities) bool { return c.Terminal }
	if got := modes.ModesWith(terminal); len(got) != 1 || got[0] != ModeTUI {
		t.Fatalf("terminal modes = %v", got)
	}
	for _, bad := range []RuntimeModes{
		{},
		{Modes: map[Mode]ModeCapabilities{"web": {}}},
		{Modes: map[Mode]ModeCapabilities{ModeTUI: {Control: "magic"}}},
		{Modes: map[Mode]ModeCapabilities{ModeTUI: {}}, Settings: map[string]SettingChange{"model": {Live: []Mode{ModeHeadless}}}},
	} {
		if bad.Validate() == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
