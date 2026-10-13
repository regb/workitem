package model

import (
	"crypto/sha256"
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

// RepositoryUpstream is the remote destination of the integration branch.
// Git remote aliases and the onboarding source are not repository identity.
type RepositoryUpstream struct {
	URL    string `json:"url"`
	Branch string `json:"branch"`
}

func (u RepositoryUpstream) Validate() error {
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
	u := &RepositoryUpstream{URL: url, Branch: branch}
	return u, u.Validate()
}
func (u RepositoryUpstream) String() string { return u.URL + " branch " + u.Branch }
func (u RepositoryUpstream) Ref() string {
	hash := sha256.Sum256([]byte(u.URL + "\x00" + u.Branch))
	return fmt.Sprintf("refs/wi/upstream/%x", hash)
}
