package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/version"
)

type Config struct {
	Executable string   `json:"executable,omitempty"`
	Extensions []string `json:"extensions,omitempty"`
	Model      string   `json:"model,omitempty"`
	Thinking   string   `json:"thinking,omitempty"`
}

func Run(ctx context.Context) error {
	socket := os.Getenv(driverprotocol.BootstrapSocketEnv)
	request, reply, err := driverprotocol.ConnectRequest(ctx, socket, driverprotocol.Hello{ProtocolVersion: driverprotocol.Version, Driver: "pi", DriverVersion: version.BuildIdentity()})
	if err != nil {
		return fmt.Errorf("connect to wi driver bootstrap: %w", err)
	}
	respondError := func(err error) error {
		_ = reply(driverprotocol.Response{ProtocolVersion: driverprotocol.Version, Error: err.Error()})
		return err
	}
	if request.ProtocolVersion != driverprotocol.Version || request.Driver != "pi" {
		return respondError(fmt.Errorf("incompatible Pi driver request"))
	}
	if request.Operation != driverprotocol.OperationLaunch {
		result, err := handleConversationOperation(ctx, request)
		if err != nil {
			return respondError(err)
		}
		return reply(driverprotocol.Response{ProtocolVersion: driverprotocol.Version, Conversation: &result})
	}
	if request.Launch == nil {
		return respondError(fmt.Errorf("Pi launch request is required"))
	}
	return launch(ctx, *request.Launch, func(ready driverprotocol.Ready) error {
		return reply(driverprotocol.Response{ProtocolVersion: driverprotocol.Version, Ready: &ready, Error: ready.Error})
	})
}

func parseConfig(raw json.RawMessage) (Config, error) {
	var config Config
	if len(raw) == 0 {
		return config, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func launch(ctx context.Context, request driverprotocol.LaunchRequest, reply func(driverprotocol.Ready) error) error {
	config, err := parseConfig(request.Config)
	if err != nil {
		_ = reply(driverprotocol.Ready{ProtocolVersion: driverprotocol.Version, Error: "invalid Pi configuration: " + err.Error()})
		return err
	}
	if config.Executable == "" {
		config.Executable = "pi"
	}
	config.Model = strings.TrimSpace(config.Model)
	config.Thinking = strings.TrimSpace(config.Thinking)
	if config.Thinking != "" && !validThinkingLevel(config.Thinking) {
		err := fmt.Errorf("invalid Pi thinking level %q", config.Thinking)
		_ = reply(driverprotocol.Ready{ProtocolVersion: driverprotocol.Version, Error: err.Error()})
		return err
	}
	resolved, err := exec.LookPath(config.Executable)
	if err != nil {
		_ = reply(driverprotocol.Ready{ProtocolVersion: driverprotocol.Version, Error: err.Error()})
		return err
	}
	mode := agent.Mode(request.Runtime.Mode)
	if !mode.Valid() {
		err := fmt.Errorf("invalid Pi runtime mode %q", mode)
		_ = reply(driverprotocol.Ready{ProtocolVersion: driverprotocol.Version, Error: err.Error()})
		return err
	}
	skillPaths, cleanup, err := exportSkills(ctx, request.Skills)
	if err != nil {
		_ = reply(driverprotocol.Ready{ProtocolVersion: driverprotocol.Version, Error: err.Error()})
		return err
	}
	defer cleanup()
	capabilities := agent.CapabilitiesForMode(mode)
	if err := reply(driverprotocol.Ready{ProtocolVersion: driverprotocol.Version, BackendPath: resolved, Capabilities: driverprotocol.Capabilities{LiveContent: true, IdleGuard: true, ImageInput: true, BundledSkills: true, NativeTUI: capabilities.NativeTUI, Headless: capabilities.Headless, Streaming: capabilities.Streaming, Prompt: true, Steering: capabilities.Steering, FollowUp: capabilities.FollowUp, Cancel: capabilities.Cancel, Monitor: capabilities.Monitor}}); err != nil {
		return err
	}
	environment := request.Environment
	if environment == nil {
		environment = map[string]string{}
	}
	environment["PI_CODING_AGENT_SESSION_DIR"] = filepath.Dir(request.Conversation.Locator)
	client := New(resolved)
	return client.ExecMode(ctx, ExecSpec{SessionPath: request.Conversation.Locator, Mode: mode, CWD: request.Runtime.CWD, LogPath: request.Runtime.LogPath, WorkItemID: request.Runtime.WorkItemID, RuntimeID: request.Runtime.ID, ControlSocketPath: request.Runtime.ControlSocketPath, EventSocketPath: request.Runtime.EventSocketPath, Env: environment, Extensions: config.Extensions, Skills: skillPaths, Model: config.Model, Thinking: config.Thinking})
}

func validThinkingLevel(value string) bool {
	switch value {
	case "off", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}
