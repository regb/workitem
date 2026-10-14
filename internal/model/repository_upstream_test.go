package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRepositoryUpstreamOptionalAndValidated(t *testing.T) {
	repo := OwnedRepository{ID: "01M4EDFH4AK5DY312SE02N0AT6", Name: "project", IntegrationBranch: "main", CreatedAt: time.Now()}
	if _, err := NormalizeOwnedRepository(repo); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{"https://example.com/project.git", "git@example.com:project.git", "/local/project.git"} {
		for _, branch := range []string{"main", "release/stable"} {
			u, err := ParseRepositoryUpstream(url, branch)
			if err != nil {
				t.Fatal(err)
			}
			if err := u.Validate(); err != nil {
				t.Fatal(err)
			}
			if _, err := NormalizeOwnedRepository(repo); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, url := range []string{"", "origin", "--upload-pack", "relative/project", "https://user:secret@example.com/project", "https://example.com/project?token=secret", "https://example.com/project\n"} {
		if _, err := ParseRepositoryUpstream(url, "main"); err == nil {
			t.Fatalf("accepted %q", url)
		}
	}
	for _, branch := range []string{"", "../escape", "main.lock", "a b", "a:refs/heads/main", "@{1}", "-main"} {
		if _, err := ParseRepositoryUpstream("https://example.com/project.git", branch); err == nil {
			t.Fatalf("accepted %q", branch)
		}
	}
	bytes, err := json.Marshal(repo)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bytes), "upstream") || strings.Contains(string(bytes), "origin_url") {
		t.Fatal(string(bytes))
	}
}
