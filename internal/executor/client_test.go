package executor

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/config"
)

func TestTerminalShellCommandForwardsGatewayPaneIdentity(t *testing.T) {
	client := Client{Name: "worker", MasterID: "master", Definition: config.ExecutorConfig{Destination: "worker", WICommand: "/worker/wi"}}
	_, shell, err := client.TerminalCommands("01KZYHGDCECSFS4BJ2SNTQP49V")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(shell, " ")
	if !strings.Contains(joined, "internal-executor-shell") || !strings.Contains(joined, "$TMUX_PANE") || !strings.Contains(joined, "01KZYHGDCECSFS4BJ2SNTQP49V") {
		t.Fatalf("shell=%q", shell)
	}
}

func TestAuthorityControlPathIsBoundedPerExecutor(t *testing.T) {
	client := Client{Name: "macbook", MasterID: strings.Repeat("master", 40), Definition: config.ExecutorConfig{Destination: "user@example.com"}}
	path := client.authorityControlPath()
	if !filepath.IsAbs(path) || len(path) >= 100 {
		t.Fatalf("control path is not portable: %q", path)
	}
	other := client
	other.Name = "linux"
	if other.authorityControlPath() == path {
		t.Fatal("different executors share an authority control path")
	}
}
