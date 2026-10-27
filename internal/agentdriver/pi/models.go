package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
)

// ModelCatalog lists the models Pi can use with the configured credentials,
// from `pi --list-models`. It starts no session.
func ModelCatalog(ctx context.Context, config Config) (driverprotocol.ModelCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, config.Executable, "--list-models").Output()
	if err != nil {
		return driverprotocol.ModelCatalog{}, fmt.Errorf("list Pi models: %w", err)
	}
	catalog := parseModelList(string(output))
	if len(catalog.Models) == 0 {
		return catalog, fmt.Errorf("Pi listed no available models")
	}
	catalog.Default, catalog.DefaultReasoning = piDefaults(catalog)
	return catalog, nil
}

// piDefaultThinkingLevel is Pi's thinking level when its settings name none.
const piDefaultThinkingLevel = "medium"

// piDefaults reads the model, when available, and thinking level Pi starts
// with from its global settings. A project's own Pi settings may still choose
// others.
func piDefaults(catalog driverprotocol.ModelCatalog) (string, string) {
	dir := os.Getenv("PI_CODING_AGENT_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", piDefaultThinkingLevel
		}
		dir = filepath.Join(home, ".pi", "agent")
	}
	var settings struct {
		Provider string `json:"defaultProvider"`
		Model    string `json:"defaultModel"`
		Thinking string `json:"defaultThinkingLevel"`
	}
	if data, err := os.ReadFile(filepath.Join(dir, "settings.json")); err == nil {
		_ = json.Unmarshal(data, &settings)
	}
	reasoning := piDefaultThinkingLevel
	if slices.Contains(driverprotocol.ReasoningLevels, settings.Thinking) {
		reasoning = settings.Thinking
	}
	value := settings.Provider + "/" + settings.Model
	for _, model := range catalog.Models {
		if settings.Provider != "" && settings.Model != "" && model.Value == value {
			return value, reasoning
		}
	}
	return "", reasoning
}

// parseModelList reads Pi's table: provider, model, context, max output,
// thinking (yes/no) and images (yes/no) columns after a header line.
func parseModelList(text string) driverprotocol.ModelCatalog {
	catalog := driverprotocol.ModelCatalog{ReasoningLevels: driverprotocol.ReasoningLevels, Models: []driverprotocol.ModelOption{}}
	scanner := bufio.NewScanner(strings.NewReader(text))
	header := true
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if header {
			header = false
			if len(fields) > 0 && fields[0] == "provider" {
				continue
			}
		}
		if len(fields) < 6 {
			continue
		}
		provider, model := fields[0], fields[1]
		catalog.Models = append(catalog.Models, driverprotocol.ModelOption{
			Value:       provider + "/" + model,
			Label:       model,
			Description: fmt.Sprintf("%s · %s context", provider, fields[2]),
			Reasoning:   fields[4] == "yes",
			ImageInput:  fields[5] == "yes",
		})
	}
	return catalog
}
