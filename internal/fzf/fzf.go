// Package fzf ranks candidates with the fzf fuzzy finder, which must be
// installed.
package fzf

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
)

// Filter ranks candidates against query, the best first, and keeps at most
// limit. No match is an empty result, not an error.
func Filter(ctx context.Context, candidates []string, query string, limit int) ([]string, error) {
	cmd := exec.CommandContext(ctx, "fzf", "--filter", query, "--read0", "--print0")
	cmd.Stdin = strings.NewReader(strings.Join(candidates, "\x00"))
	out, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		// fzf reports no match with status 1.
		return []string{}, nil
	}
	if err != nil {
		return nil, errors.New("could not search; is fzf installed?")
	}
	matches := []string{}
	for _, match := range bytes.Split(bytes.TrimRight(out, "\x00"), []byte{0}) {
		if len(matches) == limit {
			break
		}
		if len(match) > 0 {
			matches = append(matches, string(match))
		}
	}
	return matches, nil
}
