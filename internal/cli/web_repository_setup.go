package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/plugins/bundled"
	setupplugin "github.com/regb/workitem/internal/plugins/setup"
	webaccess "github.com/regb/workitem/internal/web"
)

// RepositorySetup reads a repository's effective setup settings, resolved
// like wi repo settings show, and the command its default branch declares.
func (b webBackend) RepositorySetup(ctx context.Context, project string) (webaccess.RepositorySetup, error) {
	current, err := b.cfg.Coordinator.RepositorySettings(ctx, project)
	if err != nil {
		return webaccess.RepositorySetup{}, err
	}
	effective, err := b.effectiveSettings(current.Settings)
	if err != nil {
		return webaccess.RepositorySetup{}, err
	}
	setup := webaccess.RepositorySetup{Revision: current.Settings.Revision, Skip: []string{}}
	setup.Run, _ = effective["setup"]["run"].(string)
	setup.Trust, _ = effective["setup"]["trust"].(string)
	setup.TimeoutSeconds = integerSetting(effective["setup"]["timeout_seconds"])
	setup.Skip = append(setup.Skip, stringsSetting(effective["files"]["skip"])...)
	if root, err := model.OwnedRepositoryGitDir(b.dataRoot(), current.Repository.ID); err == nil {
		setup.Declared, _ = setupplugin.DeclaredCommand(ctx, root, current.Repository.IntegrationBranch)
	}
	return setup, nil
}

// SetRepositorySetup stores the settings that changed as repository
// overrides, one fenced command each; an empty command removes the override.
func (b webBackend) SetRepositorySetup(ctx context.Context, project string, setup webaccess.RepositorySetup) webaccess.Outcome {
	current, err := b.RepositorySetup(ctx, project)
	if err != nil {
		return webaccess.Outcome{Error: err.Error()}
	}
	if current.Revision != setup.Revision {
		return webaccess.Outcome{Error: "the repository settings changed since they were read; reload and try again"}
	}
	settings, err := b.cfg.Coordinator.RepositorySettings(ctx, project)
	if err != nil {
		return webaccess.Outcome{Error: err.Error()}
	}
	revision := settings.Settings.Revision
	slices.Sort(setup.Skip)
	slices.Sort(current.Skip)
	changes := []struct {
		namespace, key string
		value, was     any
	}{
		{"plugins.setup", "run", setup.Run, current.Run},
		{"plugins.setup", "trust", setup.Trust, current.Trust},
		{"plugins.setup", "timeout_seconds", setup.TimeoutSeconds, current.TimeoutSeconds},
		{"plugins.files", "skip", setup.Skip, current.Skip},
	}
	for _, change := range changes {
		if reflect.DeepEqual(change.value, change.was) {
			continue
		}
		command := coordinator.SetRepositorySettingCommand{ID: "repository-setting:" + newCommandID(), ProtocolVersion: coordinator.ProtocolVersion, RepositoryID: settings.Repository.ID, ExpectedRevision: revision, Namespace: change.namespace, Key: change.key, CreatedAt: time.Now().UTC()}
		if run, ok := change.value.(string); ok && change.key == "run" && run == "" {
			command.Unset = true
		} else if command.Value, err = json.Marshal(change.value); err != nil {
			return webaccess.Outcome{Error: err.Error()}
		}
		result, err := b.cfg.Coordinator.SetRepositorySetting(ctx, command)
		if err != nil {
			return webaccess.Outcome{Error: fmt.Sprintf("%s.%s: %v", change.namespace, change.key, err)}
		}
		revision = result.Revision
	}
	saved, err := b.RepositorySetup(ctx, project)
	return outcome(saved, nil, err)
}

func (b webBackend) AddRepositoryFile(ctx context.Context, project string, request webaccess.RepositoryFileRequest) webaccess.Outcome {
	if request.Content == nil {
		request.Content = []byte{}
	}
	file, err := b.cfg.Coordinator.PutRepositoryFile(ctx, project, request.Path, request.Content)
	if err != nil {
		return webaccess.Outcome{Error: err.Error()}
	}
	warnings, err := b.cfg.App.SyncRepositoryFiles(ctx, project)
	return outcome(file, warnings, err)
}

func (b webBackend) RemoveRepositoryFile(ctx context.Context, project string, request webaccess.RepositoryFileRequest) webaccess.Outcome {
	if err := b.cfg.Coordinator.RemoveRepositoryFile(ctx, project, request.Path); err != nil {
		return webaccess.Outcome{Error: err.Error()}
	}
	warnings, err := b.cfg.App.SyncRepositoryFiles(ctx, project)
	return outcome(map[string]string{"removed": request.Path}, warnings, err)
}

// effectiveSettings resolves plugin settings for a repository: defaults,
// then global configuration, then the repository's overrides.
func (b webBackend) effectiveSettings(settings model.RepositorySettings) (map[string]map[string]any, error) {
	registry := bundled.Registry()
	if b.cfg.Plugins != nil {
		registry = b.cfg.Plugins.Registry.Options()
	}
	global := config.Default()
	if b.cfg.GlobalConfig != nil {
		global = *b.cfg.GlobalConfig
	}
	effective, _, err := registry.ResolveRepository(global.PluginSettings, settings.Namespaces)
	return effective, err
}

func (b webBackend) dataRoot() string {
	return filepath.Dir(b.cfg.App.Store.WorktreesDir())
}

func newCommandID() string {
	id, _ := model.NewID()
	return id
}

func integerSetting(value any) int64 {
	switch n := value.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	case json.Number:
		v, _ := n.Int64()
		return v
	}
	return 0
}

func stringsSetting(value any) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		result := []string{}
		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	return nil
}
