package model

import (
	"path/filepath"
	"testing"
)

func TestManagedSlotOwnershipIsNotGitRegistrationOrNameAlone(t *testing.T) {
	root := filepath.Join(t.TempDir(), "worktrees")
	repo := "repository-id"
	cases := []struct {
		path  string
		owned bool
	}{
		{filepath.Join(root, repo, "slot-0001"), true},
		{filepath.Join(root, repo, "slot-12000"), true},
		{filepath.Join(root, repo, "review"), false},
		{filepath.Join(root, repo, "slot-0"), false},
		{filepath.Join(root, repo, "slot-+1"), false},
		{filepath.Join(root, repo, "slot--1"), false},
		{filepath.Join(root, repo, "slot-999999999999999999999999"), false},
		{filepath.Join(root, repo, "slot-0001", "nested"), false},
		{filepath.Join(root, "another-repository", "slot-0001"), false},
		{filepath.Join(t.TempDir(), "slot-0001"), false},
	}
	for _, test := range cases {
		if got := IsManagedSlotPath(root, repo, test.path); got != test.owned {
			t.Errorf("ownership(%q)=%t want %t", test.path, got, test.owned)
		}
	}
	for _, key := range []string{"", "..", "../repository-id", "/"} {
		if IsManagedSlotPath(root, key, filepath.Join(root, repo, "slot-0001")) {
			t.Errorf("invalid repository key accepted %q", key)
		}
	}
}
