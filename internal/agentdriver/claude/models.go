package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

// ModelCatalog asks Claude Code for its model list. It opens the headless
// control protocol in an empty directory, reads the initialize response and
// exits: no session is saved and no model request is made.
func ModelCatalog(ctx context.Context, config Config) (protocol.ModelCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "wi-claude-models-")
	if err != nil {
		return protocol.ModelCatalog{}, err
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, config.Executable, "--print", "--verbose", "--input-format", "stream-json", "--output-format", "stream-json", "--no-session-persistence")
	cmd.Dir = dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return protocol.ModelCatalog{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return protocol.ModelCatalog{}, err
	}
	if err := cmd.Start(); err != nil {
		return protocol.ModelCatalog{}, fmt.Errorf("start Claude: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	request, err := json.Marshal(headlessControl("wi-models", "initialize"))
	if err != nil {
		return protocol.ModelCatalog{}, err
	}
	if _, err := stdin.Write(append(request, '\n')); err != nil {
		return protocol.ModelCatalog{}, err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		var frame struct {
			Type     string `json:"type"`
			Response struct {
				RequestID string          `json:"request_id"`
				Subtype   string          `json:"subtype"`
				Response  json.RawMessage `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil || frame.Type != "control_response" || frame.Response.RequestID != "wi-models" {
			continue
		}
		if frame.Response.Subtype != "success" {
			return protocol.ModelCatalog{}, fmt.Errorf("Claude rejected initialization")
		}
		return parseModelCatalog(frame.Response.Response)
	}
	if ctx.Err() != nil {
		return protocol.ModelCatalog{}, fmt.Errorf("Claude did not report its models in time")
	}
	return protocol.ModelCatalog{}, fmt.Errorf("Claude exited before reporting its models")
}

// parseModelCatalog reads the models of Claude's initialize response. The
// "default" entry is left out: a profile chooses a concrete model or alias.
func parseModelCatalog(raw json.RawMessage) (protocol.ModelCatalog, error) {
	var response struct {
		Models []struct {
			Value          string   `json:"value"`
			DisplayName    string   `json:"displayName"`
			Description    string   `json:"description"`
			SupportsEffort bool     `json:"supportsEffort"`
			EffortLevels   []string `json:"supportedEffortLevels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return protocol.ModelCatalog{}, fmt.Errorf("decode Claude models: %w", err)
	}
	catalog := protocol.ModelCatalog{ReasoningLevels: reasoningLevels, Models: []protocol.ModelOption{}}
	for _, model := range response.Models {
		if model.Value == "" || model.Value == "default" {
			continue
		}
		catalog.Models = append(catalog.Models, protocol.ModelOption{Value: model.Value, Label: model.DisplayName, Description: model.Description, Reasoning: model.SupportsEffort, Levels: model.EffortLevels, ImageInput: true})
	}
	if len(catalog.Models) == 0 {
		return catalog, fmt.Errorf("Claude listed no models")
	}
	return catalog, nil
}
