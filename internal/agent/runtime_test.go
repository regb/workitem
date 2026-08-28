package agent_test

import (
	"testing"

	"github.com/regb/workitem/internal/agent"
)

func TestParseModeAndCapabilities(t *testing.T) {
	for _, value := range []string{"tui", "RPC", " rpc "} {
		mode, err := agent.ParseMode(value)
		if err != nil || !mode.Valid() {
			t.Fatalf("ParseMode(%q) = %q, %v", value, mode, err)
		}
	}
	if _, err := agent.ParseMode("print"); err == nil {
		t.Fatal("expected invalid mode error")
	}
	if got := agent.CapabilitiesForMode(agent.ModeTUI); !got.NativeTUI || got.Headless || !got.Monitor {
		t.Fatalf("tui capabilities = %+v", got)
	}
	if got := agent.CapabilitiesForMode(agent.ModeRPC); got.NativeTUI || !got.Headless || !got.Steering {
		t.Fatalf("rpc capabilities = %+v", got)
	}
}
