package model

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// RepositoryInfo is the adapter-neutral result of resolving a repository and
// immutable revision for work-item creation.
type RepositoryInfo struct {
	Repository Repository
	Commit     string
}

// RepositoryHomeInfo describes Git's primary/original working checkout. A
// missing home is represented by an empty Path (for example, a bare repository).
type RepositoryHomeInfo struct {
	Path     string
	Branch   string
	Bare     bool
	Detached bool
}

// SanitizeRemoteURL removes URL components that may carry credentials before
// a remote is persisted or displayed. SCP-style SSH remotes such as
// git@example.com:owner/repository.git do not contain URL userinfo and are
// preserved.
func SanitizeRemoteURL(remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", nil
	}
	for _, value := range remote {
		if unicode.IsControl(value) {
			return "", fmt.Errorf("remote URL contains control characters")
		}
	}
	if !strings.Contains(remote, "://") {
		return remote, nil
	}
	parsed, err := url.Parse(remote)
	if err != nil {
		return "", fmt.Errorf("parse remote URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("remote URL must include a scheme and host")
	}
	if parsed.Scheme == "http" || parsed.Scheme == "https" {
		parsed.User = nil
	}
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String(), nil
}
