package daemonapp

import (
	"context"
	"sort"

	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"

	"github.com/regb/workitem/internal/agentdriver"
	"github.com/regb/workitem/internal/config"
)

func configuredDriverClients(cfg config.Config, selfPath string, environment map[string]string) map[string]agentdriver.Client {
	clients := map[string]agentdriver.Client{}
	env := make([]string, 0, len(environment))
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+environment[key])
	}
	for driverName, definition := range cfg.Drivers {
		path, args := definition.Command, []string(nil)
		if path == "" && (driverName == "pi" || driverName == "claude" || driverName == "antigravity") && selfPath != "" {
			path, args = selfPath, []string{"internal-driver", driverName}
		}
		if path != "" {
			clients[driverName] = agentdriver.Client{Driver: driverName, Path: path, Args: args, Env: env}
		}
	}
	return clients
}

// ConversationDecoders decode native conversations with the configured
// drivers, for the coordinator's conversation index.
func ConversationDecoders(cfg config.Config, selfPath string, environment map[string]string) coordinator.ConversationDecoders {
	decoders := coordinator.ConversationDecoders{}
	for name, configured := range configuredDriverClients(cfg, selfPath, environment) {
		client := configured
		decoders[name] = func(ctx context.Context, conversation model.Conversation, records []coordinator.ConversationRecord) ([]coordinator.ConversationEventFact, []bool, error) {
			lines, numbers := make([][]byte, len(records)), make([]uint64, len(records))
			for i, r := range records {
				lines[i], numbers[i] = r.Raw, r.Line
			}
			facts, decoded, err := client.DecodeConversation(ctx, conversation, numbers, lines)
			if err != nil {
				return nil, nil, err
			}
			converted := make([]coordinator.ConversationEventFact, len(facts))
			for i, f := range facts {
				converted[i] = coordinator.ConversationEventFact{Line: f.Line, Type: f.Type, Role: f.Role, Timestamp: f.Timestamp, StopReason: f.StopReason, UsageProvider: f.UsageProvider, ContentTypes: f.ContentTypes, Terminal: f.Terminal, Failed: f.Failed, BackgroundStarted: f.BackgroundStarted, BackgroundEnded: f.BackgroundEnded, BackgroundLabels: f.BackgroundLabels, BackgroundCalls: f.BackgroundCalls}
			}
			return converted, decoded, nil
		}
	}
	return decoders
}
