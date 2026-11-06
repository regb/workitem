package coordinator

import (
	"encoding/json"
	"fmt"
	"github.com/regb/workitem/internal/model"
)

const AccessRuntimeHost = "runtime-host"

// Only these internal operations are needed to execute a daemon-owned driver.
// No lifecycle, workspace, item editing, repository mutation or lease RPC is
// exposed on this endpoint.
func hostMethodAllowed(method string) bool {
	switch method {
	case MethodPing, MethodStatus, MethodCanonicalManifest, MethodListManifests, MethodRepositorySource, MethodItemResources, MethodAgentProfiles, MethodRepositorySettings, MethodRepositoryEnvironment, MethodStoreCommand, MethodRuntimeEvent:
		return true
	}
	return false
}
func (s *Server) validateHostStoreMutation(request StoreCommandRequest) error {
	if request.Command.Operation != StoreAgentRuntimeSave {
		return fmt.Errorf("runtime hosts may only publish their runtime ownership record")
	}
	var next model.AgentRuntime
	if err := json.Unmarshal(request.Payload, &next); err != nil {
		return err
	}
	var current model.AgentRuntime
	found, err := s.database.ReadProjection(RuntimeOwnershipProjection, request.Command.ItemID, &current)
	if err != nil {
		return err
	}
	if !found || current.ID != next.ID || current.WorkItemID != next.WorkItemID {
		return fmt.Errorf("runtime ownership changed")
	}
	manifest, err := s.database.CanonicalManifest(request.Command.ItemID)
	if err != nil {
		return err
	}
	claim := manifest.Manifest.Checkout.Claim
	if claim == nil || claim.ID != next.WorkspaceClaimID || claim.Generation != next.WorkspaceClaimGeneration {
		return fmt.Errorf("runtime workspace claim changed")
	}
	return nil
}

// SetRuntimeSettingsObserver installs daemon-owned handling for compact native
// setting facts. The observer must schedule work without waiting for item locks.
func (s *Server) SetRuntimeSettingsObserver(observer func(string, string, map[string]string, bool)) {
	s.runtimeSettingsObserver = observer
}
