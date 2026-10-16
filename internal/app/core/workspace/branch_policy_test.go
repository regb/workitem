package workspace

import (
	"context"
	"testing"
	"time"

	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
)

type branchPolicyGit struct {
	fakeGit
	branch string
}

func (g branchPolicyGit) CurrentBranch(context.Context, string) (string, error) { return g.branch, nil }
func (branchPolicyGit) StatusPorcelain(context.Context, string) (string, error) { return "", nil }

func TestBranchPolicyAccessAndRelease(t *testing.T) {
	for _, branch := range []string{"main", "experiment", ""} {
		t.Run(branch, func(t *testing.T) {
			path := t.TempDir()
			m := model.Manifest{ID: "item-1", State: model.StateActive, Checkout: model.Checkout{Specification: model.CheckoutSpecification{Kind: model.WorkspaceKindManagedSlot, Branch: "master"}, ClaimGeneration: 1, Claim: &model.WorkspaceClaim{ID: "test-claim", Generation: 1, Executor: model.ExecutorLocal, Path: path}}}
			resolve := func(context.Context, contract.ResolveOptions) (model.Manifest, error) { return m, nil }
			s := New(&recordingStore{}, branchPolicyGit{branch: branch}, nil, nil, resolve, nil, time.Now)
			ensured, _, err := s.EnsureCheckout(context.Background(), contract.ResolveOptions{}, m, true)
			if err != nil || ensured.Checkout.Specification.Branch != "master" {
				t.Fatalf("ensure=%+v err=%v", ensured, err)
			}
			if _, err := s.CheckoutCreate(context.Background(), contract.ResolveOptions{}); err != nil {
				t.Fatal(err)
			}
			status, err := s.Status(context.Background(), contract.ResolveOptions{})
			if err != nil {
				t.Fatal(err)
			}
			strictErr := s.ensureCheckoutBranch(context.Background(), m)
			if strictErr == nil || !status.Git.BranchMismatch {
				t.Fatalf("managed release must remain strict: status=%+v err=%v", status, strictErr)
			}
		})
	}
}
