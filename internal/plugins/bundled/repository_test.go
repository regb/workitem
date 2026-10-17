package bundled

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/regb/workitem/internal/plugin"
	"os"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/model"
)

func fixturePreparation(cfg config.Config, read plugin.RepositoryReader) plugin.Pipeline {
	catalog, err := Catalog("wi", cfg.Plugins, "")
	if err != nil {
		panic(err)
	}
	return plugin.NewHost(catalog, cfg.Plugins, cfg.PluginSettings, "wi", read).Preparation(nil)
}

func TestHostResolvesFrozenRepositoryOptionsWithoutPluginDatabaseAccess(t *testing.T) {
	cfg := config.Default()
	cfg.PluginSettings = map[string]map[string]any{"direnv": {"load": true, "trust": "prompt"}}
	reads := 0
	trust := `"always"`
	read := func(ctx context.Context, id string) (model.RepositorySettings, error) {
		reads++
		return model.RepositorySettings{RepositoryID: id, Revision: 1, Namespaces: map[string]map[string]json.RawMessage{"plugins.direnv": {"load": json.RawMessage(`false`), "trust": json.RawMessage(trust)}}}, nil
	}
	p := fixturePreparation(cfg, read)
	first, err := p.Options(context.Background(), "repo-id", "direnv")
	if err != nil || first["load"] != false || first["trust"] != "always" {
		t.Fatal(first, err)
	}
	first["trust"] = "mutated"
	trust = `"never"`
	again, err := p.Options(context.Background(), "repo-id", "direnv")
	if err != nil || reads != 1 || again["trust"] != "always" {
		t.Fatal("operation snapshot changed", again, reads, err)
	}
	next := fixturePreparation(cfg, read)
	newValue, err := next.Options(context.Background(), "repo-id", "direnv")
	if err != nil || reads != 2 || newValue["trust"] != "never" {
		t.Fatal("next operation did not refresh", newValue, reads, err)
	}
	global, err := p.Options(context.Background(), "", "direnv")
	if err != nil || global["trust"] != "prompt" {
		t.Fatal("repository policy polluted global defaults", global, err)
	}
}
func TestUnavailableRepositoryStateCannotFallBackToBroadGlobalTrust(t *testing.T) {
	cfg := config.Default()
	cfg.PluginSettings = map[string]map[string]any{"direnv": {"trust": "always"}}
	for _, read := range []plugin.RepositoryReader{nil, func(context.Context, string) (model.RepositorySettings, error) {
		return model.RepositorySettings{}, errors.New("unavailable")
	}, func(context.Context, string) (model.RepositorySettings, error) {
		return model.RepositorySettings{RepositoryID: "different"}, nil
	}} {
		p := fixturePreparation(cfg, read)
		if _, err := p.Options(context.Background(), "repo-id", "direnv"); err == nil {
			t.Fatal("lookup failure silently applied global auto-trust")
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
		if err := os.WriteFile(path, []byte("default_agent = \"pi\"\n[agents.pi]\ndriver = \"pi\"\n"+body), 0o600); err != nil {
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
