package model

import (
	"strings"
	"unicode"
)

const (
	MaxSlugLength        = 60
	BranchIDSuffixLength = 8
)

// Slugify converts a title into a conservative slug safe for branch and tmux names.
func Slugify(title string) string {
	var b strings.Builder
	lastHyphen := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		case unicode.IsSpace(r) || r == '-' || r == '_' || r == '/' || r == '.':
			if b.Len() > 0 && !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
		if b.Len() >= MaxSlugLength {
			break
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "item"
	}
	return slug
}

// NormalizeSlugSelector validates lookup syntax without turning arbitrary title
// text into a slug. It accepts canonical slug characters case-insensitively;
// callers may then use the result for exact or substring matching.
func NormalizeSlugSelector(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", false
	}
	normalized := Slugify(value)
	return normalized, normalized == value
}

// ItemBranchName returns the readable, stable implementation branch name
// allocated when an item is created. The slug is descriptive while the final
// ID characters preserve uniqueness when active slugs are eventually reused.
func ItemBranchName(slug, id string) string {
	slug = Slugify(slug)
	suffix := strings.ToLower(strings.TrimSpace(id))
	if len(suffix) > BranchIDSuffixLength {
		suffix = suffix[len(suffix)-BranchIDSuffixLength:]
	}
	if suffix == "" {
		suffix = "item"
	}
	return "wi/" + slug + "-" + suffix
}

// TerminalSessionName returns the predictable default terminal session name.
func TerminalSessionName(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		id = "item"
	}
	if len(id) > 12 {
		id = id[:12]
	}
	return "wi-" + id
}
