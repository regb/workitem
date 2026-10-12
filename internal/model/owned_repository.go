package model

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// OwnedRepository is a registered Git store. Its location is derived from its
// identity and the data root, not from the checkout used to onboard it.
type OwnedRepository struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	IntegrationBranch string    `json:"integration_branch"`
	OriginURL         string    `json:"origin_url,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	Archived          bool      `json:"archived,omitempty"`
}

// BareImportOptions is the narrow adapter contract for independent onboarding.
type BareImportOptions struct {
	Source            string
	Destination       string
	OriginURL         string
	IntegrationBranch string
	RequiredCommits   []string
}

var repositoryNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func ValidRepositoryName(name string) bool { return repositoryNamePattern.MatchString(name) }

func NormalizeOwnedRepository(r OwnedRepository) (OwnedRepository, error) {
	if !ValidID(r.ID) {
		return r, fmt.Errorf("invalid repository id %q", r.ID)
	}
	r.Name = strings.TrimSpace(r.Name)
	if !ValidRepositoryName(r.Name) {
		return r, fmt.Errorf("repository name must be 1–64 lowercase letters, digits, dots, underscores, or hyphens and start with a letter or digit")
	}
	branch := r.IntegrationBranch
	for _, character := range branch {
		if character <= ' ' || character == 127 {
			return r, fmt.Errorf("invalid integration branch %q", branch)
		}
	}
	if branch == "" || branch == "@" || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.Contains(branch, "..") || strings.Contains(branch, "//") || strings.Contains(branch, "@{") || strings.ContainsAny(branch, " ~^:?*[\\\t\r\n\x00") {
		return r, fmt.Errorf("invalid integration branch %q", branch)
	}
	for _, part := range strings.Split(branch, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, ".lock") {
			return r, fmt.Errorf("invalid integration branch %q", branch)
		}
	}
	if r.CreatedAt.IsZero() {
		return r, fmt.Errorf("repository created_at is required")
	}
	if strings.Contains(r.OriginURL, "://") {
		parsed, err := url.Parse(r.OriginURL)
		if err != nil {
			return r, err
		}
		if parsed.User != nil {
			if _, password := parsed.User.Password(); password {
				return r, fmt.Errorf("origin URL must not contain a password")
			}
		}
	}
	remote, err := SanitizeRemoteURL(r.OriginURL)
	if err != nil {
		return r, err
	}
	r.OriginURL = remote
	r.CreatedAt = r.CreatedAt.UTC()
	return r, nil
}

// ValidateRepositorySource rejects credentials before a source enters native
// Git configuration or compact onboarding history. Authentication uses helpers.
func ValidateRepositorySource(source string) error {
	clean, err := SanitizeRemoteURL(source)
	if err != nil || clean != source || clean == "" || len(source) > 4096 {
		return fmt.Errorf("valid credential-free onboarding source is required")
	}
	if strings.Contains(source, "://") {
		parsed, err := url.Parse(source)
		if err != nil {
			return err
		}
		if parsed.User != nil {
			if _, password := parsed.User.Password(); password {
				return fmt.Errorf("onboarding source must not contain a password")
			}
		}
	}
	return nil
}

func OwnedRepositoryGitDir(dataRoot, id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("invalid repository id %q", id)
	}
	if !filepath.IsAbs(dataRoot) {
		return "", fmt.Errorf("repository data root must be absolute")
	}
	return filepath.Join(dataRoot, "repositories", id+".git"), nil
}
