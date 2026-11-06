package apitest

import (
	"context"
	"encoding/json"
	p "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agentdriver/pi"
)

func (Backend) RuntimeModes(context.Context, string, json.RawMessage) (p.RuntimeModes, error) {
	return pi.RuntimeModes(), nil
}
func (Backend) ModelCatalog(context.Context, string, json.RawMessage) (p.ModelCatalog, error) {
	return p.ModelCatalog{Models: []p.ModelOption{{Value: "test/model", Label: "Test model", Reasoning: true, Levels: p.ReasoningLevels}}, ReasoningLevels: p.ReasoningLevels, Default: "test/model", DefaultReasoning: "medium"}, nil
}
