package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const ReviewGraphLimit = 200

type ReviewBranch struct{ Name, SHA, Kind string }
type ReviewGraph struct {
	Commits   []ReviewCommit
	Branches  []ReviewBranch
	Warnings  []string
	Truncated bool
}

// ReviewBranches names only the item's branch, the integration branch, and
// that integration branch's configured upstream. No ref enumeration or fetch.
func (c Client) ReviewBranches(ctx context.Context, dir, item, integration string) ([]ReviewBranch, []string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	head, err := c.ReviewResolve(ctx, dir, "refs/heads/"+item)
	if err != nil {
		return nil, nil, err
	}
	branches := []ReviewBranch{{Name: item, SHA: head, Kind: "item"}}
	warnings := []string{}
	local, err := c.reviewOptionalRef(ctx, dir, "refs/heads/"+integration)
	if err != nil {
		return nil, nil, err
	}
	branches = append(branches, ReviewBranch{Name: integration, SHA: local, Kind: "integration"})
	if local == "" {
		warnings = append(warnings, "The integration branch has no locally available commit.")
	}
	upstream, err := c.BranchUpstream(ctx, dir, integration)
	if err != nil {
		return nil, nil, err
	}
	if upstream == nil {
		warnings = append(warnings, "No upstream is configured for the integration branch.")
	} else {
		remote, err := c.reviewOptionalRef(ctx, dir, upstream.TrackingRef)
		if err != nil {
			return nil, nil, err
		}
		branches = append(branches, ReviewBranch{Name: strings.TrimPrefix(upstream.TrackingRef, "refs/remotes/"), SHA: remote, Kind: "remote"})
		if remote == "" {
			warnings = append(warnings, "The upstream has no locally cached commit. Sync the repository to update it.")
		}
	}
	return branches, warnings, nil
}

func (c Client) reviewOptionalRef(ctx context.Context, dir, ref string) (string, error) {
	out, err := c.reviewOutput(ctx, dir, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return "", nil
	}
	return strings.TrimSpace(string(out)), err
}

func (c Client) ReviewGraph(ctx context.Context, dir, item, integration string) (ReviewGraph, error) {
	branches, warnings, err := c.ReviewBranches(ctx, dir, item, integration)
	if err != nil {
		return ReviewGraph{}, err
	}
	graph := ReviewGraph{Branches: branches, Warnings: warnings}
	args := []string{"log", fmt.Sprintf("-%d", ReviewGraphLimit+1), "--topo-order", "--format=%H%x00%P%x00%s%x00%an%x00%aI%x00"}
	seen := map[string]bool{}
	for _, branch := range branches {
		if branch.SHA != "" && !seen[branch.SHA] {
			args = append(args, branch.SHA)
			seen[branch.SHA] = true
		}
	}
	out, err := c.reviewOutput(ctx, dir, append(args, "--")...)
	if err != nil {
		return graph, err
	}
	fields := strings.Split(string(out), "\x00")
	for len(fields) >= 5 {
		graph.Commits = append(graph.Commits, ReviewCommit{SHA: strings.TrimSpace(fields[0]), Parents: strings.Fields(fields[1]), Subject: fields[2], Author: fields[3], AuthoredAt: fields[4]})
		fields = fields[5:]
	}
	graph.Truncated = len(graph.Commits) > ReviewGraphLimit
	if graph.Truncated {
		graph.Commits = graph.Commits[:ReviewGraphLimit]
	}
	return graph, nil
}
