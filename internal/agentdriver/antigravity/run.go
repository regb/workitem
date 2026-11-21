// Package antigravity is the bundled driver for Google's Antigravity CLI,
// which wi runs headless through the Agent Client Protocol (`agy acp`).
// The driver keeps its own journal of each conversation, binds it to
// Antigravity's native session, and maps wi's runtime controls onto ACP.
package antigravity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	protocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agentdriver/discovery"
	"github.com/regb/workitem/internal/version"
)

// Name is the driver's name in profiles and conversations.
const Name = "antigravity"

// Config is an Antigravity profile: the CLI to run, and the model and
// reasoning level it starts with. Empty values leave Antigravity's own
// choice.
type Config struct {
	Executable string `json:"executable,omitempty"`
	Model      string `json:"model,omitempty"`
	Reasoning  string `json:"reasoning,omitempty"`
}

func parseConfig(raw json.RawMessage) (Config, error) {
	var c Config
	if len(raw) > 0 {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err := d.Decode(&c); err != nil {
			return c, err
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			return c, fmt.Errorf("one configuration object required")
		}
	}
	c.Executable = strings.TrimSpace(c.Executable)
	if c.Executable == "" {
		c.Executable = "agy"
	}
	for _, value := range []string{c.Model, c.Reasoning} {
		if strings.HasPrefix(value, "-") || strings.ContainsAny(value, "\r\n\x00") || len(value) > 256 {
			return c, fmt.Errorf("invalid Antigravity setting %q", value)
		}
	}
	return c, nil
}

// RuntimeModes reports Antigravity's one mode: headless, through ACP.
// Model and reasoning change live through the agent's session options.
func RuntimeModes() protocol.RuntimeModes {
	return protocol.RuntimeModes{
		Modes: map[protocol.Mode]protocol.ModeCapabilities{
			protocol.ModeHeadless: {Control: protocol.ControlStructured, ImageInput: true},
		},
		Settings: map[string]protocol.SettingChange{
			protocol.SettingModel:     {OnResume: true, Live: []protocol.Mode{protocol.ModeHeadless}},
			protocol.SettingReasoning: {OnResume: true, Live: []protocol.Mode{protocol.ModeHeadless}},
		},
	}
}

// Run serves one driver operation for the daemon.
func Run(ctx context.Context) error {
	request, reply, err := protocol.ConnectRequest(ctx, os.Getenv(protocol.BootstrapSocketEnv), protocol.Hello{ProtocolVersion: protocol.Version, Driver: Name, DriverVersion: version.BuildIdentity()})
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = reply(protocol.Response{ProtocolVersion: protocol.Version, Error: err.Error()})
		return err
	}
	if request.ProtocolVersion != protocol.Version || request.Driver != Name {
		return fail(fmt.Errorf("incompatible Antigravity driver request"))
	}
	if request.Operation == protocol.OperationConfigDiscover {
		result, err := discovery.Executable(Name, "Antigravity", "agy")
		if err != nil {
			return fail(err)
		}
		return reply(protocol.Response{ProtocolVersion: protocol.Version, ConfigDiscovery: &result})
	}
	config, err := parseConfig(request.Config)
	if err != nil {
		return fail(err)
	}
	switch request.Operation {
	case protocol.OperationConfigValidate:
		return reply(protocol.Response{ProtocolVersion: protocol.Version, ConfigValidation: &protocol.ConfigValidationResult{Valid: true}})
	case protocol.OperationConfigModels:
		catalog, err := ModelCatalog(ctx, config)
		if err != nil {
			return fail(err)
		}
		return reply(protocol.Response{ProtocolVersion: protocol.Version, ModelCatalog: &catalog})
	case protocol.OperationModelInvoke:
		if request.Invoke == nil {
			return fail(fmt.Errorf("model.invoke needs a request"))
		}
		text, err := Invoke(ctx, config, *request.Invoke)
		if err != nil {
			return fail(err)
		}
		return reply(protocol.Response{ProtocolVersion: protocol.Version, Invoke: &protocol.ModelInvokeResult{Text: text}})
	case protocol.OperationRuntimeModes:
		modes := RuntimeModes()
		return reply(protocol.Response{ProtocolVersion: protocol.Version, RuntimeModes: &modes})
	case protocol.OperationLaunch:
		if request.Launch == nil {
			return fail(fmt.Errorf("Antigravity launch request is required"))
		}
		config, err = parseConfig(request.Launch.Config)
		if err != nil {
			return fail(err)
		}
		return launch(ctx, *request.Launch, config, func(ready protocol.Ready) error {
			return reply(protocol.Response{ProtocolVersion: protocol.Version, Ready: &ready, Error: ready.Error})
		})
	}
	result, err := conversationOperation(ctx, request)
	if err != nil {
		return fail(err)
	}
	return reply(protocol.Response{ProtocolVersion: protocol.Version, Conversation: &result})
}

// historyLimit is the most messages one history page holds.
const historyLimit = 200

func conversationOperation(ctx context.Context, r protocol.Request) (protocol.ConversationResponse, error) {
	var result protocol.ConversationResponse
	if r.Conversation == nil {
		return result, fmt.Errorf("conversation request required")
	}
	c := r.Conversation
	switch r.Operation {
	case protocol.OperationConversationCapabilities:
		result.Capabilities = &protocol.ConversationCapabilities{History: true, HistoryPaging: true}
	case protocol.OperationConversationCreate:
		if c.ID == "" || strings.ContainsAny(c.ID, "/\\\x00\r\n") {
			return result, fmt.Errorf("invalid conversation identity")
		}
		digest := sha256.Sum256([]byte("wi-antigravity:" + c.ID))
		result.Conversation = protocol.Conversation{ID: c.ID, Locator: fmt.Sprintf("sessions/antigravity/%x.jsonl", digest[:16]), Format: journalFormat}
	case protocol.OperationConversationCWD:
		_, cwd, err := latestSession(c.Path)
		result.CWD = cwd
		return result, err
	case protocol.OperationConversationFork:
		return result, forkJournal(c.SourcePath, c.TargetPath, c.TargetCWD)
	case protocol.OperationConversationObserve:
		observed, warnings := observe(c.Path, c.Now)
		result.Observation, result.Warnings = &observed, warnings
	case protocol.OperationConversationDecode:
		result.Decoded = make([]bool, len(c.Lines))
		for i, line := range c.Lines {
			number := c.StartLine + uint64(i)
			if len(c.LineNumbers) == len(c.Lines) {
				number = c.LineNumbers[i]
			}
			var record Record
			if json.Unmarshal(line, &record) != nil || record.Type == "" {
				continue
			}
			result.Decoded[i] = true
			if f, ok := fact(number, record); ok {
				result.Facts = append(result.Facts, f)
			}
		}
	case protocol.OperationConversationHistory:
		if c.Subagent != "" {
			return result, fmt.Errorf("Antigravity history has no subagent conversations")
		}
		history, err := historyPage(c.Path, c.BeforeID, historyLimit)
		if err != nil {
			return result, err
		}
		result.History = &history
	default:
		return result, fmt.Errorf("unsupported Antigravity operation %q", r.Operation)
	}
	return result, nil
}
