package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/model"
)

func TestControlPlanTakesVacantLeaseSilentlyAndConfirmsInterruptions(t *testing.T) {
	structuredTUI := model.Channels{Observe: true, Control: model.ControlStructured, AtomicIdle: true, Terminal: true}
	emulatedTUI := model.Channels{Observe: true, Control: model.ControlEmulated, Terminal: true}
	headless := model.Channels{Observe: true, Control: model.ControlStructured}
	web := ControlRequest{Holder: model.LeaseHolderWeb, ClientID: "tab", EnsureRuntime: true}
	terminal := ControlRequest{Holder: model.LeaseHolderTerminal, ClientID: "terminal"}
	otherTab := &model.ControlLease{Holder: model.LeaseHolderWeb, ClientID: "other"}
	for _, tc := range []struct {
		name    string
		status  ControlStatus
		request ControlRequest
		want    ControlPlan
	}{
		{"vacant structured runtime", ControlStatus{RuntimeOnline: true, RuntimeMode: "tui", Channels: structuredTUI}, web, ControlPlan{}},
		{"own lease renews", ControlStatus{Lease: &model.ControlLease{Holder: model.LeaseHolderWeb, ClientID: "tab"}, RuntimeOnline: true, Channels: headless, Busy: true}, web, ControlPlan{}},
		{"offline runtime starts headless", ControlStatus{Channels: model.Channels{Observe: true}}, web, ControlPlan{Start: "headless"}},
		{"vacant idle keystroke TUI is replaced silently", ControlStatus{RuntimeOnline: true, RuntimeMode: "tui", Channels: emulatedTUI}, web, ControlPlan{Replace: "headless"}},
		{"busy replacement aborts", ControlStatus{RuntimeOnline: true, RuntimeMode: "tui", Channels: emulatedTUI, Busy: true}, web, ControlPlan{Replace: "headless", Abort: true, NeedsConfirmation: true}},
		{"attached terminal is displaced", ControlStatus{Holder: model.LeaseHolderTerminal, Implicit: true, RuntimeOnline: true, RuntimeMode: "tui", Channels: structuredTUI}, web, ControlPlan{DisplacesTerminal: true, NeedsConfirmation: true}},
		{"other tab is displaced and its turn aborted", ControlStatus{Lease: otherTab, Holder: model.LeaseHolderWeb, RuntimeOnline: true, Channels: headless, Busy: true}, web, ControlPlan{Displaces: otherTab, Abort: true, NeedsConfirmation: true}},
		{"terminal replaces idle headless runtime", ControlStatus{RuntimeOnline: true, RuntimeMode: "headless", Channels: headless}, terminal, ControlPlan{Replace: "tui"}},
		{"terminal over web lease", ControlStatus{Lease: otherTab, Holder: model.LeaseHolderWeb, RuntimeOnline: true, RuntimeMode: "headless", Channels: headless}, terminal, ControlPlan{Displaces: otherTab, Replace: "tui", NeedsConfirmation: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := (&App{}).planControl(tc.status, tc.request, legacyRuntimeModes())
			if (got.Displaces == nil) != (tc.want.Displaces == nil) || got.DisplacesTerminal != tc.want.DisplacesTerminal || got.Abort != tc.want.Abort || got.Replace != tc.want.Replace || got.Start != tc.want.Start || got.NeedsConfirmation != tc.want.NeedsConfirmation {
				t.Fatalf("plan = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestControlPlanUsesTheModesTheDriverReports(t *testing.T) {
	type caps = driverprotocol.ModeCapabilities
	modes := func(m map[driverprotocol.Mode]caps) driverprotocol.RuntimeModes {
		return driverprotocol.RuntimeModes{Modes: m}
	}
	emulatedTUI := model.Channels{Observe: true, Control: model.ControlEmulated, Terminal: true}
	headless := model.Channels{Observe: true, Control: model.ControlStructured}
	web := ControlRequest{Holder: model.LeaseHolderWeb, ClientID: "tab", EnsureRuntime: true}
	terminal := ControlRequest{Holder: model.LeaseHolderTerminal, ClientID: "terminal", EnsureRuntime: true}
	tuiOnly := modes(map[driverprotocol.Mode]caps{driverprotocol.ModeTUI: {Control: driverprotocol.ControlEmulated, Terminal: true}})
	headlessOnly := modes(map[driverprotocol.Mode]caps{driverprotocol.ModeHeadless: {Control: driverprotocol.ControlStructured}})
	structuredTUI := modes(map[driverprotocol.Mode]caps{driverprotocol.ModeTUI: {Control: driverprotocol.ControlStructured, Terminal: true}})
	for _, tc := range []struct {
		name    string
		status  ControlStatus
		request ControlRequest
		modes   driverprotocol.RuntimeModes
		want    ControlPlan
	}{
		{"keystroke-only driver cannot give the web control", ControlStatus{RuntimeOnline: true, RuntimeMode: "tui", Channels: emulatedTUI}, web, tuiOnly, ControlPlan{Unavailable: "structured"}},
		{"offline keystroke-only driver is not started for the web", ControlStatus{}, web, tuiOnly, ControlPlan{Unavailable: "structured"}},
		{"headless-only driver cannot give a terminal", ControlStatus{RuntimeOnline: true, RuntimeMode: "headless", Channels: headless}, terminal, headlessOnly, ControlPlan{Unavailable: "terminal"}},
		{"the web starts the mode that has structured control", ControlStatus{}, web, structuredTUI, ControlPlan{Start: "tui"}},
		{"a runtime with the channel is kept whatever the driver reports", ControlStatus{RuntimeOnline: true, RuntimeMode: "headless", Channels: headless}, web, tuiOnly, ControlPlan{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := (&App{}).planControl(tc.status, tc.request, tc.modes)
			if got.Start != tc.want.Start || got.Replace != tc.want.Replace || (got.Unavailable == "") != (tc.want.Unavailable == "") || !strings.Contains(got.Unavailable, tc.want.Unavailable) {
				t.Fatalf("plan = %+v, want %+v", got, tc.want)
			}
		})
	}
}

type modesBackend struct {
	ConversationBackend
	calls int
	modes driverprotocol.RuntimeModes
	err   error
}

func (b *modesBackend) RuntimeModes(context.Context, string, json.RawMessage) (driverprotocol.RuntimeModes, error) {
	b.calls++
	return b.modes, b.err
}

func TestAgentRuntimeModesAsksTheDriverOnceAndFallsBack(t *testing.T) {
	reported := driverprotocol.RuntimeModes{Modes: map[driverprotocol.Mode]driverprotocol.ModeCapabilities{driverprotocol.ModeTUI: {Control: driverprotocol.ControlStructured, Terminal: true}}}
	backend := &modesBackend{modes: reported}
	a := &App{DefaultAgent: "pi", AgentsConfig: map[string]config.AgentDefinitionConfig{"pi": {Driver: "pi"}}, DriversConfig: map[string]config.DriverDefinitionConfig{"pi": {}}, ConversationBackends: map[string]ConversationBackend{"pi": backend}}
	for i := 0; i < 3; i++ {
		if got := a.AgentRuntimeModes(context.Background(), "pi"); len(got.Modes) != 1 || got.Modes[driverprotocol.ModeTUI].Control != driverprotocol.ControlStructured {
			t.Fatalf("modes = %+v", got)
		}
	}
	if backend.calls != 1 {
		t.Fatalf("driver asked %d times, want once", backend.calls)
	}
	old := &modesBackend{err: errors.New("unsupported operation")}
	b := &App{DefaultAgent: "pi", AgentsConfig: map[string]config.AgentDefinitionConfig{"pi": {Driver: "pi"}}, DriversConfig: map[string]config.DriverDefinitionConfig{"pi": {}}, ConversationBackends: map[string]ConversationBackend{"pi": old}}
	if got := b.AgentRuntimeModes(context.Background(), "pi"); got.Modes[driverprotocol.ModeHeadless].Control != driverprotocol.ControlStructured || !got.Modes[driverprotocol.ModeTUI].Terminal {
		t.Fatalf("older driver did not fall back to legacy modes: %+v", got)
	}
}
