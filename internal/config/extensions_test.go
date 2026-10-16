package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/extensions/bundled"
	"github.com/regb/workitem/internal/options"
)

const configRepoID = "01KZYHGDCECSFS4BJ2SNTQP49V"

func TestRegisteredGlobalConfigResolution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, "[extensions.direnv]\nload = false\n")
	cfg, warnings, err := config.LoadWithRegistry(path, bundled.Registry())
	if err != nil || len(warnings) != 0 {
		t.Fatalf("warnings=%v err=%v", warnings, err)
	}
	values := cfg.ExtensionSettings(bundled.Registry())
	if values["direnv"]["load"] != false || values["direnv"]["trust"] != "prompt" {
		t.Fatal(values)
	}
	if cfg.ExtensionOrigins()["extensions.direnv.load"] != "user" || cfg.ExtensionOrigins()["extensions.direnv.trust"] != "default" {
		t.Fatal(cfg.ExtensionOrigins())
	}
}

func TestRegisteredConfigurationRejectsInvalidOptionsAndBindings(t *testing.T) {
	for _, body := range []string{
		`[extensions.direnv]
trust = "typo"`,
		`[extensions.direnv]
load = "true"`,
		`[extensions.direnv]
command = "execute this"`,
		`[repositories.workitem.extensions.direnv]
trust = "always"`,
		`[repositories."` + configRepoID + `"]
command = "execute this"`,
		`[extensions.missing]
load = true`,
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		writeFile(t, path, body)
		if _, _, err := config.LoadWithRegistry(path, bundled.Registry()); err == nil {
			t.Fatal(body)
		}
	}
	for _, body := range []string{`[extensions.direnv]
trust = "always"`, `[repositories."` + configRepoID + `".extensions.direnv]
load = true`} {
		if _, _, err := config.ReadProject(strings.NewReader(body)); err == nil {
			t.Fatal("project elevated operator policy")
		}
	}
}
func TestUnregisteredOptionsRemainInspectableButNotEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, "[extensions.future]\nload = false\napi_key = \"private\"\n")
	cfg, warnings, err := config.LoadWithRegistry(path, bundled.Registry())
	if err != nil || len(warnings) != 1 || cfg.Extensions["future"]["api_key"] != "private" {
		t.Fatalf("warnings=%v err=%v", warnings, err)
	}
}
func TestRegistryIsSuppliedByCompositionNotCore(t *testing.T) {
	registry, err := options.New(options.Definition{Name: "example", Options: []options.Option{{Name: "value", Type: "string", Default: "default", Repository: true}, {Name: "command", Type: "string", Default: "", Secret: true}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, "[extensions.example]\nvalue = \"custom\"\n")
	cfg, _, err := config.LoadWithRegistry(path, registry)
	if err != nil || cfg.Extensions["example"]["value"] != "custom" {
		t.Fatal(cfg.Extensions, err)
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
