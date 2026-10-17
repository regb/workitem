package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/plugin"
	"github.com/regb/workitem/internal/plugins/bundled"
)

func pluginConfigFixture(t *testing.T) string {
	t.Helper()
	path := configQueryFixture(t)
	manifest := filepath.Join(filepath.Dir(path), "demo.json")
	data := `{"version":1,"name":"demo","command":["demo","--token","command-private"],"commands":["run"],"options":[{"name":"api_token","type":"string","default":"","secret":true},{"name":"visible","type":"bool","default":true}]}`
	if err := os.WriteFile(manifest, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("\n[plugins.demo]\nmanifest = " + quote(manifest) + "\ncommand = [\"demo\",\"--token\",\"command-private\"]\n[extensions.demo]\napi_token = \"plugin-private\"\n")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	return path
}
func quote(s string) string { raw, _ := json.Marshal(s); return string(raw) }
func TestPluginSecretsUseSharedSchemaAndInspectionOptIn(t *testing.T) {
	path := pluginConfigFixture(t)
	for _, args := range [][]string{{"show"}, {"get", "extensions.demo"}} {
		for _, jsonOut := range []bool{false, true} {
			var out, stderr bytes.Buffer
			if code := runConfigMain(args, path, &out, &stderr, jsonOut); code != ExitOK {
				t.Fatal(code, stderr.String())
			}
			if strings.Contains(out.String(), "plugin-private") || strings.Contains(out.String(), "command-private") {
				t.Fatal("default config inspection exposed secrets")
			}
		}
	}
	_, host, _, err := bundled.Load(path, "wi")
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	cfg := Config{Plugins: host, Stdout: &out, Stderr: &stderr}
	if err := runPlugin(context.Background(), []string{"list"}, cfg, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "plugin-private") || strings.Contains(out.String(), "command-private") || !strings.Contains(out.String(), `"visible": true`) {
		t.Fatal("plugin list did not apply schema redaction")
	}
	out.Reset()
	if err := runPlugin(context.Background(), []string{"list", "--show-sensitive"}, cfg, true); err != nil || !strings.Contains(out.String(), "plugin-private") || !strings.Contains(out.String(), "command-private") {
		t.Fatal("explicit inspection failed", err)
	}
	settings, err := host.Settings(context.Background(), "", "demo")
	if err != nil || settings["api_token"] != "plugin-private" {
		t.Fatal("inspection mutated execution settings", err)
	}
}
func TestAgentCannotManuallyRunScheduledPlugin(t *testing.T) {
	registry, err := plugin.NewRegistry(plugin.Definition{Name: "demo", Command: []string{"must-not-execute"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, access := range []string{coordinator.AccessAgent, ""} {
		cfg := Config{Plugins: plugin.NewHost(registry, nil, nil, "wi", nil), ControlAccess: access, Env: map[string]string{}}
		if access == "" {
			cfg.Env["WI_AGENT_DAEMON_SOCKET"] = "/restricted.sock"
		}
		if err := runPlugin(context.Background(), []string{"run", "demo"}, cfg, false); err == nil || !strings.Contains(err.Error(), "agent endpoint") {
			t.Fatal("manual run lost caller restriction", err)
		}
	}
}
