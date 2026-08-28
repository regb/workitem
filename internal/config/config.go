package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/priority"
)

const (
	DefaultMaxDeepWorkActive = 2
	DefaultRepositoryFolders = 2
)

type Config struct {
	DeepWork    DeepWorkConfig    `json:"deep_work"`
	Item        ItemConfig        `json:"item"`
	List        ListConfig        `json:"list"`
	AgentStatus AgentStatusConfig `json:"agent_status"`
	Attention   AttentionConfig   `json:"attention"`
	Direnv      DirenvConfig      `json:"direnv"`
}

type ProjectConfig struct {
	Item ItemConfig `json:"item"`
}

type ItemConfig struct {
	Defaults ItemDefaultsConfig `json:"defaults"`
}

type ItemDefaultsConfig struct {
	Labels []string `json:"labels,omitempty"`
}

type DeepWorkConfig struct {
	MaxActive int `json:"max_active"`
}

type ListConfig struct {
	RepositoryFolders int      `json:"repository_folders"`
	Labels            []string `json:"labels,omitempty"`
}

type AttentionConfig struct {
	Priority string `json:"priority"`
}

type AgentStatusConfig struct {
	Markers AgentStatusMarkersConfig `json:"markers"`
}

type AgentStatusMarkersConfig struct {
	Busy    string `json:"busy,omitempty"`
	Idle    string `json:"idle,omitempty"`
	Problem string `json:"problem,omitempty"`
}

type DirenvConfig struct {
	// AutoTrustRepositories is a user-configured allowlist. Repository config
	// must never be able to opt itself into execution trust.
	AutoTrustRepositories []string `json:"auto_trust_repositories,omitempty"`
}

func Default() Config {
	return Config{DeepWork: DeepWorkConfig{MaxActive: DefaultMaxDeepWorkActive}, List: ListConfig{RepositoryFolders: DefaultRepositoryFolders}, Attention: AttentionConfig{Priority: priority.DefaultStrategy}}
}

func Load(path string) (Config, []string, error) {
	cfg := Default()
	if strings.TrimSpace(path) == "" {
		return cfg, nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil, nil
		}
		return cfg, nil, fmt.Errorf("open config: %w", err)
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
			warnings = append(warnings, fmt.Sprintf("line %d ignored: expected key = value", lineNo))
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch section + "." + key {
		case "deep_work.max_active":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return cfg, warnings, fmt.Errorf("parse deep_work.max_active on line %d: %w", lineNo, err)
			}
			cfg.DeepWork.MaxActive = parsed
		case "item.defaults.labels":
			parsed, err := parseStringArray(value)
			if err != nil {
				return cfg, warnings, fmt.Errorf("parse item.defaults.labels on line %d: %w", lineNo, err)
			}
			cfg.Item.Defaults.Labels = parsed
		case "list.repository_folders":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return cfg, warnings, fmt.Errorf("parse list.repository_folders on line %d: %w", lineNo, err)
			}
			cfg.List.RepositoryFolders = parsed
		case "list.labels":
			parsed, err := parseStringArray(value)
			if err != nil {
				return cfg, warnings, fmt.Errorf("parse list.labels on line %d: %w", lineNo, err)
			}
			cfg.List.Labels = parsed
		case "attention.priority":
			cfg.Attention.Priority = parseStringValue(value)
		case "agent_status.markers.busy":
			cfg.AgentStatus.Markers.Busy = parseStringValue(value)
		case "agent_status.markers.idle":
			cfg.AgentStatus.Markers.Idle = parseStringValue(value)
		case "agent_status.markers.problem":
			cfg.AgentStatus.Markers.Problem = parseStringValue(value)
		case "direnv.auto_trust_repositories":
			parsed, err := parseStringArray(value)
			if err != nil {
				return cfg, warnings, fmt.Errorf("parse direnv.auto_trust_repositories on line %d: %w", lineNo, err)
			}
			cfg.Direnv.AutoTrustRepositories = parsed
		default:
			warnings = append(warnings, fmt.Sprintf("line %d ignored: unknown key %s.%s", lineNo, section, key))
		}
	}
	if err := scanner.Err(); err != nil {
		return cfg, warnings, fmt.Errorf("read config: %w", err)
	}
	return cfg, warnings, Validate(cfg)
}

func Validate(cfg Config) error {
	if cfg.DeepWork.MaxActive < 0 {
		return fmt.Errorf("deep_work.max_active must be nonnegative")
	}
	if _, err := model.NormalizeLabels(cfg.Item.Defaults.Labels); err != nil {
		return fmt.Errorf("item.defaults.labels: %w", err)
	}
	if cfg.List.RepositoryFolders < 1 {
		return fmt.Errorf("list.repository_folders must be at least 1")
	}
	if strings.TrimSpace(cfg.Attention.Priority) == "" {
		return fmt.Errorf("attention.priority must not be empty")
	}
	if _, err := priority.Rank(cfg.Attention.Priority, nil); err != nil {
		return err
	}
	for _, repository := range cfg.Direnv.AutoTrustRepositories {
		if !filepath.IsAbs(strings.TrimSpace(repository)) {
			return fmt.Errorf("direnv.auto_trust_repositories entries must be absolute paths: %q", repository)
		}
	}
	return nil
}

func parseStringArray(s string) ([]string, error) {
	raw := strings.TrimSpace(s)
	if len(raw) < 2 || raw[0] != '[' || raw[len(raw)-1] != ']' {
		return nil, fmt.Errorf("expected an array of quoted strings")
	}
	body := strings.TrimSpace(raw[1 : len(raw)-1])
	body = strings.TrimSpace(strings.TrimSuffix(body, ","))
	raw = "[" + body + "]"
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("expected an array of quoted strings: %w", err)
	}
	return values, nil
}

func parseStringValue(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if unquoted, err := strconv.Unquote(s); err == nil {
			return unquoted
		}
	}
	return s
}

func stripComment(s string) string {
	inQuote := false
	for i, r := range s {
		switch r {
		case '"':
			inQuote = !inQuote
		case '#':
			if !inQuote {
				return strings.TrimSpace(s[:i])
			}
		}
	}
	return s
}
