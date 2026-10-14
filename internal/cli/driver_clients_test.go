package cli

import (
	"reflect"
	"testing"

	"github.com/regb/workitem/internal/config"
)

func TestBundledDriverClients(t *testing.T) {
	cfg := config.Default()
	clients := configuredDriverClients(cfg, "/tmp/wi", nil)
	for _, driver := range []string{"pi", "claude"} {
		client, ok := clients[driver]
		if !ok || client.Path != "/tmp/wi" || !reflect.DeepEqual(client.Args, []string{"internal-driver", driver}) {
			t.Fatalf("%s: %+v", driver, client)
		}
	}
	cfg.Drivers["claude"] = config.DriverDefinitionConfig{Command: "/trusted/custom-driver"}
	client := configuredDriverClients(cfg, "/tmp/wi", nil)["claude"]
	if client.Path != "/trusted/custom-driver" || len(client.Args) != 0 {
		t.Fatalf("explicit command ignored: %+v", client)
	}
	if len(cfg.Agents) != 0 {
		t.Fatalf("profiles: %+v", cfg.Agents)
	}
}
