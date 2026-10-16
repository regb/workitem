package app

import (
	"context"
	"testing"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/extension"
	"github.com/regb/workitem/internal/model"
)

func TestRuntimeEnvironmentHooksReceiveIdentityInBothModes(t *testing.T) {
	t.Setenv("EXTENSION_TEST_REMOVE", "stale")
	for _, mode := range []agent.Mode{agent.ModeTUI, agent.ModeHeadless} {
		calls := 0
		a := &App{Tmux: fakeBaseTmux{global: map[string]string{"PATH": "/bin", "HOME": "/home/operator", "EXTENSION_TEST_REMOVE": "stale"}}}
		a.Extensions = extension.Pipeline{Hooks: []extension.Hooks{{Name: "fixture", Sanitize: func(context.Context, extension.EnvironmentRequest, map[string]any) (extension.Patch, error) {
			return extension.Patch{Warnings: []string{"sanitizer warning"}}, nil
		}, RuntimeEnvironment: func(ctx context.Context, r extension.EnvironmentRequest, _ map[string]any) (extension.Patch, error) {
			calls++
			if r.Subject.RepositoryID != "repo" || r.Subject.ItemID != "item" || r.Subject.Mode != string(mode) || r.Subject.ClaimID != "claim" || r.Subject.Generation != 7 {
				t.Fatal(r.Subject)
			}
			return extension.Patch{Set: map[string]string{"HOOK_VALUE": "ready"}, Unset: []string{"EXTENSION_TEST_REMOVE"}}, nil
		}}}}
		m := model.Manifest{ID: "item", Repository: model.Repository{ID: "repo"}, Checkout: model.Checkout{ClaimGeneration: 7, Claim: &model.WorkspaceClaim{ID: "claim", Generation: 7, Path: t.TempDir()}}}
		env, _, warnings, err := a.agentRuntimeEnvironment(context.Background(), m, mode)
		if err != nil || calls != 1 || env["HOOK_VALUE"] != "ready" || env["EXTENSION_TEST_REMOVE"] != "" || len(warnings) != 1 {
			t.Fatalf("mode=%s calls=%d err=%v", mode, calls, err)
		}
	}
}
