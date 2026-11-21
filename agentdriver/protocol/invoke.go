package driverprotocol

import "fmt"

// OperationModelInvoke sends one prompt to a model and returns its answer.
// It is a bare model call, not an agent turn: no tools, no workspace (it runs
// in an empty directory), no session or conversation kept, and Instructions
// in place of the agent's own system prompt. A driver comes as close to that
// as its backend allows and documents where it falls short. The model and
// reasoning level come from the request's config, like a launch's.
const OperationModelInvoke = "model.invoke"

// Bounds of a model invocation.
const (
	MaxInvokePromptBytes = 64 << 10
	MaxInvokeOutputBytes = 16 << 10
)

// ModelInvokeRequest is one prompt and the instructions to answer it with.
type ModelInvokeRequest struct {
	Instructions string `json:"instructions,omitempty"`
	Prompt       string `json:"prompt"`
}

// Validate checks the request is bounded and has a prompt.
func (r ModelInvokeRequest) Validate() error {
	if r.Prompt == "" {
		return fmt.Errorf("model invocation needs a prompt")
	}
	if len(r.Prompt)+len(r.Instructions) > MaxInvokePromptBytes {
		return fmt.Errorf("model invocation prompt exceeds %d bytes", MaxInvokePromptBytes)
	}
	return nil
}

// ModelInvokeResult is the model's answer.
type ModelInvokeResult struct {
	Text string `json:"text"`
}
