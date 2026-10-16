package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
	"github.com/regb/workitem/internal/model"
)

const ProjectConfigRelativePath = ".config/wi.toml"

func LoadProjectForRepository(root string) (ProjectConfig, []string, error) {
	return LoadProject(filepath.Join(root, ProjectConfigRelativePath))
}
func LoadProject(path string) (ProjectConfig, []string, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return ProjectConfig{}, nil, nil
	}
	if err != nil {
		return ProjectConfig{}, nil, fmt.Errorf("open project config: %w", err)
	}
	defer f.Close()
	return ReadProject(f)
}

// Project files contain declarative defaults only. They cannot register code,
// grant execution trust, or override operator-owned extension policy.
func ReadProject(reader io.Reader) (ProjectConfig, []string, error) {
	var cfg ProjectConfig
	data, err := io.ReadAll(io.LimitReader(reader, 1<<20+1))
	if err != nil {
		return cfg, nil, err
	}
	if len(data) > 1<<20 {
		return cfg, nil, fmt.Errorf("project config exceeds 1 MiB")
	}
	root := map[string]any{}
	if err := toml.Unmarshal(data, &root); err != nil {
		return cfg, nil, fmt.Errorf("parse project config: %w", err)
	}
	for _, key := range []string{"extensions", "repositories"} {
		if _, ok := root[key]; ok {
			return cfg, nil, fmt.Errorf("project configuration cannot set operator-owned %s", key)
		}
	}
	// Other sections remain ignored; only the safe subset is decoded.
	safe := map[string]any{"item": root["item"]}
	b, err := json.Marshal(safe)
	if err != nil {
		return cfg, nil, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, nil, err
	}
	if _, err := model.NormalizeLabels(cfg.Item.Defaults.Labels); err != nil {
		return cfg, nil, fmt.Errorf("project item.defaults.labels: %w", err)
	}
	return cfg, nil, nil
}
