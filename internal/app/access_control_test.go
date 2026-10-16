package app

import (
	"testing"

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
			got := (&App{}).planControl(tc.status, tc.request)
			if (got.Displaces == nil) != (tc.want.Displaces == nil) || got.DisplacesTerminal != tc.want.DisplacesTerminal || got.Abort != tc.want.Abort || got.Replace != tc.want.Replace || got.Start != tc.want.Start || got.NeedsConfirmation != tc.want.NeedsConfirmation {
				t.Fatalf("plan = %+v, want %+v", got, tc.want)
			}
		})
	}
}
