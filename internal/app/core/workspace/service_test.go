package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
)

type fakeGit struct{}

func (fakeGit) Head(context.Context, string) (string, error)                { return "abc", nil }
func (fakeGit) StatusPorcelain(context.Context, string) (string, error)     { return " M file", nil }
func (fakeGit) CurrentBranch(context.Context, string) (string, error)       { return "wi/item-1", nil }
func (fakeGit) BranchExists(context.Context, string, string) (bool, error)  { return false, nil }
func (fakeGit) WorktreeAdd(context.Context, model.WorktreeAddOptions) error { return nil }
func (fakeGit) Switch(context.Context, string, string, string, bool) error  { return nil }
func (fakeGit) CommitAll(context.Context, string, string) (string, error)   { return "", nil }

type recordingStore struct {
	saved  model.Manifest
	events []model.Event
}

func (s *recordingStore) SaveManifest(_ context.Context, m model.Manifest) error {
	s.saved = m
	return nil
}
func (s *recordingStore) AppendEvent(_ context.Context, _ string, event model.Event) error {
	s.events = append(s.events, event)
	return nil
}
func (*recordingStore) LoadAgentRuntime(string) (*model.AgentRuntime, error) { return nil, nil }
func (*recordingStore) ListManifests() ([]model.Manifest, []error)           { return nil, nil }
func (*recordingStore) WorktreesDir() string                                 { return "" }

func TestCheckoutPreparationCanStopClaimPublication(t *testing.T) {
	called := false
	s := New(nil, fakeGit{}, nil, func(ctx context.Context, r contract.CheckoutPreparation) ([]string, error) {
		called = true
		if !r.Reused || r.Generation != 4 {
			t.Fatal(r)
		}
		return nil, fmt.Errorf("preparation failed")
	}, nil, nil, nil)
	m := model.Manifest{Checkout: model.Checkout{ClaimGeneration: 3}}
	result, err := s.finishSlotAssignment(context.Background(), m, "/slot", "branch", "source", nil, true)
	if err == nil || !called || result.Manifest.Checkout.Claim != nil {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestStatusReportsAbsentCheckout(t *testing.T) {
	resolve := func(context.Context, contract.ResolveOptions) (model.Manifest, error) {
		return model.Manifest{ID: "item-1", State: model.StateBacklog, Checkout: model.Checkout{Specification: model.CheckoutSpecification{Branch: "wi/item-1"}}}, nil
	}
	res, err := New(nil, fakeGit{}, nil, nil, resolve, nil, nil).Status(context.Background(), contract.ResolveOptions{})
	if err != nil || len(res.Warnings) != 1 || res.Warnings[0] != "checkout is absent" || res.Git.ExpectedBranch != "wi/item-1" {
		t.Fatalf("result=%+v err=%v", res, err)
	}
}

func TestStatusReportsUnavailableCheckout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	resolve := func(context.Context, contract.ResolveOptions) (model.Manifest, error) {
		return model.Manifest{ID: "item-1", State: model.StateActive, Checkout: model.Checkout{Specification: model.CheckoutSpecification{Branch: "wi/item-1"}, ClaimGeneration: 1, Claim: &model.WorkspaceClaim{ID: "test-claim", Generation: 1, Executor: model.ExecutorLocal, Path: path}}}, nil
	}
	res, err := New(nil, fakeGit{}, nil, nil, resolve, nil, nil).Status(context.Background(), contract.ResolveOptions{})
	if err != nil || len(res.Warnings) != 1 || res.Git.CurrentHead != "" {
		t.Fatalf("result=%+v err=%v", res, err)
	}
}

func TestStatusInspectsPresentCheckout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Clean(dir)
	resolve := func(context.Context, contract.ResolveOptions) (model.Manifest, error) {
		return model.Manifest{ID: "item-1", State: model.StateActive, Checkout: model.Checkout{Specification: model.CheckoutSpecification{Branch: "wi/item-1"}, ClaimGeneration: 1, Claim: &model.WorkspaceClaim{ID: "test-claim", Generation: 1, Executor: model.ExecutorLocal, Path: path}}}, nil
	}
	res, err := New(nil, fakeGit{}, nil, nil, resolve, nil, nil).Status(context.Background(), contract.ResolveOptions{})
	if err != nil || !res.Git.BranchMatches || !res.Git.Dirty || res.Git.CurrentHead != "abc" {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
}
