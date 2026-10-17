package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/options"
	"github.com/regb/workitem/internal/plugins/bundled"
)

const configRepoID = "01KZYHGDCECSFS4BJ2SNTQP49V"

func TestRegisteredGlobalConfigResolution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, "[plugins.direnv]\nload = false\n")
	cfg, warnings, err := config.LoadWithRegistry(path, bundled.Registry())
	if err != nil || len(warnings) != 0 {
		t.Fatalf("warnings=%v err=%v", warnings, err)
	}
	values := cfg.ResolvedPluginSettings(bundled.Registry())
	if values["direnv"]["load"] != false || values["direnv"]["trust"] != "prompt" {
		t.Fatal(values)
	}
	if cfg.PluginOrigins()["plugins.direnv.load"] != "user" || cfg.PluginOrigins()["plugins.direnv.trust"] != "default" {
		t.Fatal(cfg.PluginOrigins())
	}
}

func TestRegisteredConfigurationRejectsInvalidOptionsAndBindings(t *testing.T) {
	for _, body := range []string{
		`[plugins.direnv]
trust = "typo"`,
		`[plugins.direnv]
load = "true"`,
		`[plugins.direnv]
exec = "execute this"`,
		`[extensions.direnv]
load = false`,
		`[repositories.workitem.plugins.direnv]
trust = "always"`,
		`[repositories."` + configRepoID + `"]
command = "execute this"`,
		`[plugins.missing]
load = true`,
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		writeFile(t, path, body)
		if _, _, err := config.LoadWithRegistry(path, bundled.Registry()); err == nil {
			t.Fatal(body)
		}
	}
	for _, body := range []string{`[plugins.direnv]
trust = "always"`, `[extensions.direnv]
trust = "always"`, `[repositories."` + configRepoID + `".plugins.direnv]
load = true`} {
		if _, _, err := config.ReadProject(strings.NewReader(body)); err == nil {
			t.Fatal("project elevated operator policy")
		}
	}
}
func TestUnregisteredOptionsRemainInspectableButNotEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, "[plugins.future]\nload = false\napi_key = \"private\"\n")
	cfg, warnings, err := config.LoadWithRegistry(path, bundled.Registry())
	if err != nil || len(warnings) != 1 || cfg.PluginSettings["future"]["api_key"] != "private" {
		t.Fatalf("warnings=%v err=%v", warnings, err)
	}
}
func TestRegistryIsSuppliedByCompositionNotCore(t *testing.T) {
	registry, err := options.New(options.Definition{Name: "example", Options: []options.Option{{Name: "value", Type: "string", Default: "default", Repository: true}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, "[plugins.example]\nenabled = true\nvalue = \"custom\"\n")
	cfg, _, err := config.LoadWithRegistry(path, registry)
	if err != nil || cfg.PluginSettings["example"]["value"] != "custom" || !cfg.Plugins["example"].Enabled {
		t.Fatal(cfg.PluginSettings, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = config.LoadWithRegistry(path, registry)
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("loading modified config")
	}
}
