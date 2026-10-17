// Package bundled composes the common catalog for native and executable plugins.
package bundled

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/regb/workitem/internal/config"
	adapter "github.com/regb/workitem/internal/direnv"
	"github.com/regb/workitem/internal/options"
	"github.com/regb/workitem/internal/plugin"
	direnvplugin "github.com/regb/workitem/internal/plugins/direnv"
	githubplugin "github.com/regb/workitem/plugins/github"
)

func Catalog(self string, registrations map[string]plugin.Registration, configDir string) (*plugin.Registry, error) {
	schema := direnvplugin.Definition()
	min, max := int64(30), int64(86400)
	definitions := []plugin.Definition{
		{Name: schema.Name, Options: schema.Options, RetiredSections: schema.RetiredSections, Required: true, Native: func(approve plugin.Approver) plugin.Hooks { return direnvplugin.New(adapter.New("direnv"), approve) }},
		{Name: "github", Options: []options.Option{{Name: "ignore_authors", Type: "strings", Default: []string{}, Repository: true, Description: "Authors whose comments or reviews do not satisfy a wait."}, {Name: "interval_seconds", Type: "integer", Default: int64(300), Min: &min, Max: &max, Description: "Polling interval in seconds."}}, Commands: []string{"wait", "sync"}, Jobs: []plugin.Job{{Name: "check", Args: []string{"sync"}, IntervalOption: "interval_seconds", Repository: true}}, Command: []string{filepath.Join(filepath.Dir(self), "wi-github")}, Instructions: githubplugin.Skill},
	}
	known := map[string]bool{"direnv": true, "github": true}
	for name, registration := range registrations {
		if known[name] {
			if registration.Manifest != "" {
				return nil, fmt.Errorf("bundled plugin %s cannot be replaced by a manifest", name)
			}
			if name == "direnv" {
				if registration.EnabledSet && !registration.Enabled || len(registration.Command) > 0 {
					return nil, fmt.Errorf("direnv cleanup must stay registered; use plugins.direnv.load = false to disable evaluation")
				}
			}
			continue
		}
		if registration.Manifest == "" {
			// A settings-only table does not install a plugin. Preserve its
			// inactive options for inspection without inventing a definition.
			if !registration.Enabled && len(registration.Command) == 0 {
				continue
			}
			return nil, fmt.Errorf("plugin %s requires an explicit declarative manifest", name)
		}
		path := registration.Manifest
		if !filepath.IsAbs(path) {
			path = filepath.Join(configDir, path)
		}
		definition, err := plugin.LoadManifest(path)
		if err != nil {
			return nil, fmt.Errorf("plugin %s manifest: %w", name, err)
		}
		if definition.Name != name {
			return nil, fmt.Errorf("plugin manifest identity mismatch for %s", name)
		}
		definitions = append(definitions, definition)
	}
	return plugin.NewRegistry(definitions...)
}
func Registry() *options.Registry {
	catalog, err := Catalog("wi", nil, "")
	if err != nil {
		panic(err)
	}
	return catalog.Options()
}

// Load inspects manifests before typed configuration validation. No executable
// runs while building the shared catalog or querying its schemas.
func Load(path, self string) (config.Config, *plugin.Host, []string, error) {
	registrations, err := config.PluginRegistrations(path)
	if err != nil {
		return config.Config{}, nil, nil, err
	}
	catalog, err := Catalog(self, registrations, filepath.Dir(path))
	if err != nil {
		return config.Config{}, nil, nil, err
	}
	cfg, warnings, err := config.LoadWithRegistry(path, catalog.Options())
	if err != nil {
		return cfg, nil, warnings, err
	}
	a, _ := json.Marshal(registrations)
	b, _ := json.Marshal(cfg.Plugins)
	if string(a) != string(b) {
		return cfg, nil, warnings, fmt.Errorf("plugin registration changed while loading configuration; retry")
	}
	return cfg, plugin.NewHost(catalog, cfg.Plugins, cfg.PluginSettings, self, nil), warnings, nil
}
