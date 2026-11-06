package web

import (
	"net/url"
	"regexp"
	"strings"
)

var scpRemote = regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):(.+)$`)

// NewRemote describes an upstream URL for display: credentials removed, and
// for a host it knows, the repository's web page.
func NewRemote(raw, branch string) *Remote {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	remote := &Remote{URL: raw, Branch: branch}
	host, path := "", ""
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		parsed.User = nil
		remote.URL = parsed.String()
		host, path = parsed.Hostname(), parsed.Path
	} else if match := scpRemote.FindStringSubmatch(raw); match != nil && !strings.Contains(raw, "://") {
		// git@github.com:owner/repo.git
		remote.URL = match[1] + ":" + match[2]
		host, path = match[1], match[2]
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	for _, known := range []string{"github", "gitlab", "bitbucket"} {
		if host == known+".com" || host == known+".org" {
			remote.Host = known
		}
	}
	if remote.Host != "" && strings.Count(path, "/") >= 1 {
		remote.WebURL = "https://" + host + "/" + path
		if branch != "" {
			switch remote.Host {
			case "github":
				remote.WebURL += "/tree/" + branch
			case "gitlab":
				remote.WebURL += "/-/tree/" + branch
			case "bitbucket":
				remote.WebURL += "/src/" + branch
			}
		}
	}
	return remote
}
