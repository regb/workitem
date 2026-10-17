package plugin

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvironmentDropsItemContextButPreservesAuthority(t *testing.T) {
	env := Environment([]string{"PATH=/bin", "WI_ID=item", "WI_AGENT_DAEMON_SOCKET=/restricted", "TMUX=/tmp/x", "TMUX_PANE=%1", "HOME=/h"}, Spec{Name: "github", Settings: map[string]any{"ignore_authors": []string{"codecov"}}}, "/usr/bin/wi")
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "WI_ID=") || strings.Contains(joined, "TMUX=") || !strings.Contains(joined, "WI_EXECUTABLE=/usr/bin/wi") || !strings.Contains(joined, "WI_AGENT_DAEMON_SOCKET=/restricted") || !strings.Contains(joined, `WI_PLUGIN_CONFIG={"ignore_authors":["codecov"]}`) {
		t.Fatal("incorrect context/authority sanitization")
	}
}
func TestRunOnceReportsOutputAndFailure(t *testing.T) {
	script := filepath.Join(t.TempDir(), "plugin")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$WI_PLUGIN via $WI_EXECUTABLE\"\nexit 3\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := RunOnce(context.Background(), Spec{Name: "demo", Command: []string{script}}, "/usr/bin/wi", &out, &out)
	if err == nil || out.String() != "demo via /usr/bin/wi\n" {
		t.Fatal(err, out.String())
	}
}
func TestSkillUsesStaticAssetWithoutExecutingPlugin(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "executed")
	spec := Spec{Name: "demo", Command: []string{"touch", marker}, Instructions: "Static instructions."}
	text, err := Skill(context.Background(), spec, "wi")
	if err != nil || text != "Static instructions." {
		t.Fatal(text, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("inspection executed plugin")
	}
}
