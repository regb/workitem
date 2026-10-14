package model

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ValidateRepositoryBranch validates an ordinary branch name, not a revision
// expression or a fetch refspec.
func ValidateRepositoryBranch(branch string) error {
	for _, character := range branch {
		if character <= ' ' || character == 127 {
			return fmt.Errorf("invalid integration branch %q", branch)
		}
	}
	if branch == "" || branch == "@" || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.Contains(branch, "..") || strings.Contains(branch, "//") || strings.Contains(branch, "@{") || strings.ContainsAny(branch, " ~^:?*[\\\t\r\n\x00") {
		return fmt.Errorf("invalid integration branch %q", branch)
	}
	for _, part := range strings.Split(branch, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, ".lock") {
			return fmt.Errorf("invalid integration branch %q", branch)
		}
	}
	return nil
}

// RepositoryUpstream is a Git configuration observation, not registry intent.
// URL and branch also form the input to an explicit tracking configuration edit.
type RepositoryUpstream struct {
	URL         string `json:"url"`
	Branch      string `json:"branch"`
	Remote      string `json:"remote,omitempty"`
	TrackingRef string `json:"tracking_ref,omitempty"`
}

func (u RepositoryUpstream) Validate() error {
	if u.Remote != "" && (u.Remote == "." || strings.Contains(u.Remote, "/") || ValidateRepositoryBranch(u.Remote) != nil) {
		return fmt.Errorf("upstream remote must be a simple named remote")
	}
	if u.TrackingRef != "" && (u.Remote == "" || !strings.HasPrefix(u.TrackingRef, "refs/remotes/") || ValidateRepositoryBranch(strings.TrimPrefix(u.TrackingRef, "refs/remotes/")) != nil) {
		return fmt.Errorf("upstream tracking ref must be under refs/remotes/")
	}
	if strings.HasPrefix(u.URL, "-") || (!strings.Contains(u.URL, ":") && !filepath.IsAbs(u.URL)) {
		return fmt.Errorf("upstream must be a Git URL or absolute repository path")
	}
	if err := ValidateRepositorySource(u.URL); err != nil {
		return fmt.Errorf("invalid upstream URL: %w", err)
	}
	err := ValidateRepositoryBranch(u.Branch)
	if err != nil || len(u.Branch) > 256 {
		return fmt.Errorf("invalid upstream branch %q", u.Branch)
	}
	return nil
}
func ParseRepositoryUpstream(url, branch string) (*RepositoryUpstream, error) {
	u := &RepositoryUpstream{URL: url, Branch: branch, Remote: "origin", TrackingRef: "refs/remotes/origin/" + branch}
	return u, u.Validate()
}
func (u RepositoryUpstream) String() string { return u.URL + " branch " + u.Branch }
func (u RepositoryUpstream) Ref() string {
	if u.TrackingRef != "" {
		return u.TrackingRef
	}
	remote := u.Remote
	if remote == "" {
		remote = "origin"
	}
	return "refs/remotes/" + remote + "/" + u.Branch
}
