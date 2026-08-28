package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/xdg"
)

func TestInfoPrintsSingleValueWithoutStartingDaemon(t *testing.T) {
	paths, err := xdg.Resolve(xdg.Map{
		"XDG_DATA_HOME":   filepath.Join(t.TempDir(), "data"),
		"XDG_CONFIG_HOME": filepath.Join(t.TempDir(), "config"),
		"XDG_CACHE_HOME":  filepath.Join(t.TempDir(), "cache"),
		"XDG_STATE_HOME":  filepath.Join(t.TempDir(), "state"),
		"XDG_RUNTIME_DIR": filepath.Join(t.TempDir(), "runtime"),
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runInfoMain([]string{"agent-socket"}, paths, &stdout, &stderr, false); code != ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	if got == "" || !strings.HasSuffix(got, filepath.Join(paths.DataRootKey, "daemon.sock")) {
		t.Fatalf("agent socket = %q", got)
	}
}

func TestInfoJSONIncludesResolvedRootsAndSockets(t *testing.T) {
	paths, err := xdg.Resolve(xdg.Map{"XDG_DATA_HOME": filepath.Join(t.TempDir(), "data"), "XDG_RUNTIME_DIR": filepath.Join(t.TempDir(), "runtime")}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runInfoMain(nil, paths, &stdout, &stderr, true); code != ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var got map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"data_root", "database", "config_file", "cache_root", "state_root", "runtime_root", "operator_socket", "agent_socket"} {
		if got[key] == "" {
			t.Fatalf("missing %s in %+v", key, got)
		}
	}
}

func TestInfoRejectsUnknownKey(t *testing.T) {
	paths, err := xdg.Resolve(xdg.Map{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runInfoMain([]string{"unknown"}, paths, &stdout, &stderr, false); code != ExitUsage || !strings.Contains(stderr.String(), "unknown info key") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}
