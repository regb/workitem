package model

import (
	"fmt"
	"strings"
	"unicode"
)

func NormalizeLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return "", fmt.Errorf("label must not be empty")
	}
	var b strings.Builder
	lastSep := false
	for _, r := range strings.ToLower(label) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-', r == '.', r == '/':
			b.WriteRune(r)
			lastSep = false
		case unicode.IsSpace(r):
			if b.Len() > 0 && !lastSep {
				b.WriteByte('-')
				lastSep = true
			}
		default:
			return "", fmt.Errorf("label %q contains unsafe character %q", label, r)
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "", fmt.Errorf("label must not be empty")
	}
	return out, nil
}

func NormalizeLabels(labels []string) ([]string, error) {
	out := make([]string, 0, len(labels))
	seen := map[string]bool{}
	for _, label := range labels {
		normalized, err := NormalizeLabel(label)
		if err != nil {
			return nil, err
		}
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, normalized)
	}
	return out, nil
}

func AddLabels(existing []string, labels ...string) ([]string, []string, error) {
	out, err := NormalizeLabels(existing)
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, label := range out {
		seen[label] = true
	}
	added := []string{}
	for _, label := range labels {
		normalized, err := NormalizeLabel(label)
		if err != nil {
			return nil, nil, err
		}
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, normalized)
		added = append(added, normalized)
	}
	return out, added, nil
}

func RemoveLabels(existing []string, labels ...string) ([]string, []string, error) {
	remove := map[string]bool{}
	for _, label := range labels {
		normalized, err := NormalizeLabel(label)
		if err != nil {
			return nil, nil, err
		}
		remove[normalized] = true
	}
	out := []string{}
	removed := []string{}
	for _, label := range existing {
		normalized, err := NormalizeLabel(label)
		if err != nil {
			return nil, nil, err
		}
		if remove[normalized] {
			removed = append(removed, normalized)
			continue
		}
		out = append(out, normalized)
	}
	return out, removed, nil
}

func HasLabel(labels []string, label string) bool {
	normalized, err := NormalizeLabel(label)
	if err != nil {
		return false
	}
	for _, existing := range labels {
		if existing == normalized {
			return true
		}
	}
	return false
}
