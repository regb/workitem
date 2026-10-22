package pi

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
)

// thinkingLevels are Pi's --thinking values, clamped by Pi to each model.
var thinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

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
	return catalog, nil
}

// parseModelList reads Pi's table: provider, model, context, max output,
// thinking (yes/no) and images (yes/no) columns after a header line.
func parseModelList(text string) driverprotocol.ModelCatalog {
	catalog := driverprotocol.ModelCatalog{ModelSetting: "model", ReasoningSetting: "thinking", ReasoningLevels: thinkingLevels, Models: []driverprotocol.ModelOption{}}
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
