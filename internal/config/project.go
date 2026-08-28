package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/regb/workitem/internal/model"
)

const ProjectConfigRelativePath = ".config/wi.toml"

func LoadProjectForRepository(repositoryRoot string) (ProjectConfig, []string, error) {
	return LoadProject(filepath.Join(repositoryRoot, ProjectConfigRelativePath))
}

// LoadProject reads the safe declarative subset of repository configuration.
// Unknown keys are ignored so command-oriented project configuration can be
// added separately without making item creation execute or approve anything.
func LoadProject(path string) (ProjectConfig, []string, error) {
	cfg := ProjectConfig{}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil, nil
		}
		return cfg, nil, fmt.Errorf("open project config: %w", err)
	}
	defer f.Close()

	section := ""
	warnings := []string{}
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := stripComment(strings.TrimSpace(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if section+"."+strings.TrimSpace(key) != "item.defaults.labels" {
			continue
		}
		labels, err := parseStringArray(strings.TrimSpace(value))
		if err != nil {
			return cfg, warnings, fmt.Errorf("parse item.defaults.labels in project config on line %d: %w", lineNo, err)
		}
		cfg.Item.Defaults.Labels = labels
	}
	if err := scanner.Err(); err != nil {
		return cfg, warnings, fmt.Errorf("read project config: %w", err)
	}
	if _, err := model.NormalizeLabels(cfg.Item.Defaults.Labels); err != nil {
		return cfg, warnings, fmt.Errorf("project item.defaults.labels: %w", err)
	}
	return cfg, warnings, nil
}
