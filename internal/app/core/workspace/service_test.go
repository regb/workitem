package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

type fakeGit struct {
	repository model.RepositoryInfo
}

func (f fakeGit) DetectRepository(context.Context, string, string) (model.RepositoryInfo, error) {
	return f.repository, nil
}
func (fakeGit) Head(context.Context, string) (string, error)                { return "abc", nil }
func (fakeGit) StatusPorcelain(context.Context, string) (string, error)     { return " M file", nil }
func (fakeGit) CurrentBranch(context.Context, string) (string, error)       { return "wi/item-1", nil }
func (fakeGit) BranchExists(context.Context, string, string) (bool, error)  { return false, nil }
func (fakeGit) WorktreeAdd(context.Context, model.WorktreeAddOptions) error { return nil }
func (fakeGit) Switch(context.Context, string, string, string, bool) error  { return nil }
func (fakeGit) WorktreeRemove(context.Context, string, string, bool) error  { return nil }

type recordingStore struct {
	saved  model.Manifest
	events []model.Event
}

func (s *recordingStore) SaveManifest(_ context.Context, m model.Manifest) error {
	s.saved = m
	return nil
}
func (*recordingStore) ClaimRepositoryHome(context.Context, model.Manifest) error { return nil }
func (s *recordingStore) AppendEvent(_ context.Context, _ string, event model.Event) error {
	s.events = append(s.events, event)
	return nil
}
func (*recordingStore) LoadAgentRuntime(string) (*model.AgentRuntime, error) { return nil, nil }
func (*recordingStore) ListManifests() ([]model.Manifest, []error)           { return nil, nil }
func (*recordingStore) WorktreesDir() string                                 { return "" }

func TestRelocateRepositoryPreservesCreationProvenance(t *testing.T) {
	manifest := model.Manifest{
		ID: "item-1",
		Repository: model.Repository{
			RootAtCreation:    "/old/repo",
			GitCommonDir:      "/old/repo/.git",
			RemoteURL:         "git@example.com:acme/repo.git",
			CreatedFromCommit: "abc",
		},
		Checkout: model.Checkout{Kind: model.WorkspaceKindManagedSlot, Branch: "wi/item-1"},
	}
	resolve := func(context.Context, contract.ResolveOptions) (model.Manifest, error) { return manifest, nil }
	st := &recordingStore{}
	git := fakeGit{repository: model.RepositoryInfo{Repository: model.Repository{RootAtCreation: "/new/repo", RemoteURL: manifest.Repository.RemoteURL, CreatedFromCommit: "abc"}, Commit: "abc"}}
	result, err := New(st, git, nil, nil, resolve, nil, func() time.Time { return time.Unix(10, 0).UTC() }).RelocateRepository(context.Background(), contract.ResolveOptions{}, "/new/repo")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.PreviousRoot != "/old/repo" || result.CurrentRoot != "/new/repo" {
		t.Fatalf("result=%+v", result)
	}
	if st.saved.Repository.RootAtCreation != "/old/repo" || st.saved.Repository.GitCommonDir != "/old/repo/.git" || st.saved.Repository.CurrentRoot != "/new/repo" {
		t.Fatalf("saved repository=%+v", st.saved.Repository)
	}
	if len(st.events) != 1 || st.events[0].Type != "repository.relocated" {
		t.Fatalf("events=%+v", st.events)
	}
}

func TestRelocateRepositoryRejectsDifferentOrigin(t *testing.T) {
	manifest := model.Manifest{ID: "item-1", Repository: model.Repository{RootAtCreation: "/old/repo", RemoteURL: "git@example.com:acme/repo.git", CreatedFromCommit: "abc"}, Checkout: model.Checkout{Kind: model.WorkspaceKindManagedSlot, Branch: "wi/item-1"}}
	resolve := func(context.Context, contract.ResolveOptions) (model.Manifest, error) { return manifest, nil }
	git := fakeGit{repository: model.RepositoryInfo{Repository: model.Repository{RootAtCreation: "/other/repo", RemoteURL: "git@example.com:other/repo.git"}}}
	if _, err := New(&recordingStore{}, git, nil, nil, resolve, nil, time.Now).RelocateRepository(context.Background(), contract.ResolveOptions{}, "/other/repo"); err == nil || !strings.Contains(err.Error(), "does not match recorded origin") {
		t.Fatalf("err=%v", err)
	}
}

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
