package app

import (
	"context"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/model"
)

func TestRuntimeCannotResumeAgainstReplacementWorkspaceClaim(t *testing.T) {
	manifest := model.Manifest{ID: "item-1", Checkout: model.Checkout{
		Specification:   model.CheckoutSpecification{ExecutorProfile: ""},
		ClaimGeneration: 2,
		Claim:           &model.WorkspaceClaim{ID: "claim-2", Generation: 2, ExecutorProfile: "", Path: "/workspace"},
	}}
	runtime := model.AgentRuntime{ID: "runtime-1", WorkspaceClaimID: "claim-1", WorkspaceClaimGeneration: 1}
	if _, err := (&App{}).validateRuntimeCheckout(context.Background(), manifest, runtime); err == nil || !strings.Contains(err.Error(), "superseded workspace claim") {
		t.Fatalf("err=%v", err)
	}
}
