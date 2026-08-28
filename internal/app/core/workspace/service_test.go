package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
)

type fakeDirenv struct {
	status model.DirenvStatus
	denied string
}

func (d *fakeDirenv) Status(context.Context, string) (model.DirenvStatus, error) {
	return d.status, nil
}
func (d *fakeDirenv) Deny(_ context.Context, path string) error {
	d.denied = path
	return nil
}

type fakeGit struct{}

func (fakeGit) Head(context.Context, string) (string, error)                { return "abc", nil }
func (fakeGit) StatusPorcelain(context.Context, string) (string, error)     { return " M file", nil }
func (fakeGit) CurrentBranch(context.Context, string) (string, error)       { return "wi/item-1", nil }
func (fakeGit) BranchExists(context.Context, string, string) (bool, error)  { return false, nil }
func (fakeGit) WorktreeAdd(context.Context, model.WorktreeAddOptions) error { return nil }
func (fakeGit) Switch(context.Context, string, string, string, bool) error  { return nil }
func (fakeGit) WorktreeRemove(context.Context, string, string, bool) error  { return nil }
func TestReusedSlotRevokesExistingDirenvTrust(t *testing.T) {
	direnv := &fakeDirenv{status: model.DirenvStatus{Found: true, Allowed: true, RCPath: "/slot/.envrc"}}
	s := New(nil, fakeGit{}, nil, direnv, nil, nil, nil)
	revoked, err := s.revokeReusedSlotDirenvTrust(context.Background(), "/slot")
	if err != nil || !revoked || direnv.denied != "/slot/.envrc" {
		t.Fatalf("revoked=%v denied=%q err=%v", revoked, direnv.denied, err)
	}
}

func TestStatusReportsAbsentCheckout(t *testing.T) {
	resolve := func(context.Context, contract.ResolveOptions) (model.Manifest, error) {
		return model.Manifest{ID: "item-1", State: model.StateBacklog, Checkout: model.Checkout{Branch: "wi/item-1"}}, nil
	}
	res, err := New(nil, fakeGit{}, nil, nil, resolve, nil, nil).Status(context.Background(), contract.ResolveOptions{})
	if err != nil || len(res.Warnings) != 1 || res.Warnings[0] != "checkout is absent" || res.Git.ExpectedBranch != "wi/item-1" {
		t.Fatalf("result=%+v err=%v", res, err)
	}
}

func TestStatusReportsUnavailableCheckout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	resolve := func(context.Context, contract.ResolveOptions) (model.Manifest, error) {
		return model.Manifest{ID: "item-1", State: model.StateWorking, Checkout: model.Checkout{Path: &path, Branch: "wi/item-1"}}, nil
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
		return model.Manifest{ID: "item-1", State: model.StateWorking, Checkout: model.Checkout{Path: &path, Branch: "wi/item-1"}}, nil
	}
	res, err := New(nil, fakeGit{}, nil, nil, resolve, nil, nil).Status(context.Background(), contract.ResolveOptions{})
	if err != nil || !res.Git.BranchMatches || !res.Git.Dirty || res.Git.CurrentHead != "abc" {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
}
