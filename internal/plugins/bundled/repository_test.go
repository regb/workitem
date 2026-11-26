package bundled

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/plugin"
)

func fixturePreparation(cfg config.Config, read plugin.RepositoryReader) plugin.Pipeline {
	catalog, err := Catalog("wi", cfg.Plugins, "")
	if err != nil {
		panic(err)
	}
	return plugin.NewHost(catalog, cfg.Plugins, cfg.PluginSettings, "wi", read).Preparation()
}

// Repository settings override the global ones for the bundled plugins; the
// snapshot an operation reads once (see plugin's
// TestNativeAndExecutableUseOneSettingsResolver) is not aliased by its callers.
func TestHostResolvesRepositoryOptionsForBundledPlugins(t *testing.T) {
	cfg := config.Default()
	cfg.PluginSettings = map[string]map[string]any{"setup": {"trust": "never"}, "direnv": {"load": true}}
	read := func(ctx context.Context, id string) (model.RepositorySettings, error) {
		return model.RepositorySettings{RepositoryID: id, Revision: 1, Namespaces: map[string]map[string]json.RawMessage{"plugins.setup": {"trust": json.RawMessage(`"always"`)}, "plugins.direnv": {"load": json.RawMessage(`false`)}}}, nil
	}
	p := fixturePreparation(cfg, read)
	first, err := p.Options(context.Background(), "repo-id", "setup")
	if err != nil || first["trust"] != "always" {
		t.Fatal(first, err)
	}
	if direnv, err := p.Options(context.Background(), "repo-id", "direnv"); err != nil || direnv["load"] != false {
		t.Fatal(direnv, err)
	}
	first["trust"] = "mutated"
	if again, err := p.Options(context.Background(), "repo-id", "setup"); err != nil || again["trust"] != "always" {
		t.Fatal("caller mutated the operation snapshot", again, err)
	}
	global, err := p.Options(context.Background(), "", "setup")
	if err != nil || global["trust"] != "never" {
		t.Fatal("repository policy polluted global defaults", global, err)
	}
}
func TestUnavailableRepositoryStateCannotFallBackToGlobalOptions(t *testing.T) {
	cfg := config.Default()
	cfg.PluginSettings = map[string]map[string]any{"setup": {"trust": "always"}}
	for _, read := range []plugin.RepositoryReader{nil, func(context.Context, string) (model.RepositorySettings, error) {
		return model.RepositorySettings{}, errors.New("unavailable")
	}, func(context.Context, string) (model.RepositorySettings, error) {
		return model.RepositorySettings{RepositoryID: "different"}, nil
	}} {
		p := fixturePreparation(cfg, read)
		if _, err := p.Options(context.Background(), "repo-id", "setup"); err == nil {
			t.Fatal("lookup failure silently applied global options")
		}
	}
}

func TestRetiredRepositorySettingIsIgnored(t *testing.T) {
	read := func(ctx context.Context, id string) (model.RepositorySettings, error) {
		return model.RepositorySettings{RepositoryID: id, Revision: 1, Namespaces: map[string]map[string]json.RawMessage{"plugins.direnv": {"trust": json.RawMessage(`"prompt"`), "load": json.RawMessage(`false`)}}}, nil
	}
	values, err := fixturePreparation(config.Default(), read).Options(context.Background(), "repo-id", "direnv")
	if err != nil || values["load"] != false || values["trust"] != nil {
		t.Fatal(values, err)
	}
}

func TestUnavailablePluginOptionsSurviveCatalogLoading(t *testing.T) {
	for _, activation := range []string{"", "enabled = false\n"} {
		t.Run(activation, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			body := "[plugins.future]\n" + activation + "load = false\napi_key = \"private\"\n"
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, host, warnings, err := Load(path, "wi")
			if err != nil {
				t.Fatal(err)
			}
			if len(warnings) != 1 || cfg.PluginSettings["future"]["api_key"] != "private" {
				t.Fatal("inactive settings were lost", warnings)
			}
			specs, err := host.Specs(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			for _, spec := range specs {
				if spec.Name == "future" {
					t.Fatal("settings-only table installed a plugin")
				}
			}
		})
	}
}
func TestUnavailablePluginCannotActivateWithoutManifest(t *testing.T) {
	for _, activation := range []string{"enabled = true", "command = \"do-not-run\"", "load = true"} {
		path := filepath.Join(t.TempDir(), "config.toml")
		body := "[plugins.future]\n" + activation + "\n"
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := Load(path, "wi"); err == nil {
			t.Fatalf("unavailable plugin accepted activation: %s", activation)
		}
	}
}

func TestDirenvTableTakesOptionsButCannotBeDisabledOrReplaced(t *testing.T) {
	dir := t.TempDir()
	for body, ok := range map[string]bool{
		"[plugins.direnv]\nload = false\n":                 true,
		"[plugins.direnv]\nenabled = true\nload = false\n": true,
		"[plugins.direnv]\nenabled = false\n":              false,
		"[plugins.direnv]\ncommand = \"direnv-wrapper\"\n": false,
	} {
		path := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, _, _, err := Load(path, "wi")
		if (err == nil) != ok {
			t.Fatalf("%q: err = %v", body, err)
		}
		if ok && cfg.PluginSettings["direnv"]["load"] != false {
			t.Fatalf("%q: settings = %v", body, cfg.PluginSettings)
		}
	}
}
