package bundled

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/model"
)

func TestHostResolvesFrozenRepositoryOptionsWithoutPluginDatabaseAccess(t *testing.T) {
	cfg := config.Default()
	cfg.Extensions = map[string]map[string]any{"direnv": {"load": true, "trust": "prompt"}}
	reads := 0
	trust := `"always"`
	read := func(ctx context.Context, id string) (model.RepositorySettings, error) {
		reads++
		return model.RepositorySettings{RepositoryID: id, Revision: 1, Namespaces: map[string]map[string]json.RawMessage{"extensions.direnv": {"load": json.RawMessage(`false`), "trust": json.RawMessage(trust)}}}, nil
	}
	p := New(cfg, nil, read)
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
	next := New(cfg, nil, read)
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
	cfg.Extensions = map[string]map[string]any{"direnv": {"trust": "always"}}
	for _, read := range []RepositoryReader{nil, func(context.Context, string) (model.RepositorySettings, error) {
		return model.RepositorySettings{}, errors.New("unavailable")
	}, func(context.Context, string) (model.RepositorySettings, error) {
		return model.RepositorySettings{RepositoryID: "different"}, nil
	}} {
		p := New(cfg, nil, read)
		if _, err := p.Options(context.Background(), "repo-id", "direnv"); err == nil {
			t.Fatal("lookup failure silently applied global auto-trust")
		}
	}
}
