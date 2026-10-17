// Package github is an optional wi plugin. It finds waiting items whose
// blockers reference GitHub pull requests and clears those blockers when the
// pull request is reviewed, merged, or closed. It uses only the wi and gh
// command-line interfaces; wi's core knows nothing about GitHub.
package github

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// Skill is the Markdown this plugin adds to the wi skill when it is enabled.
const Skill = `wi checks GitHub pull requests for waiting items every few minutes with the gh CLI.

When the user asks you to make the item wait on a pull request, use:

` + "```bash" + `
wi github wait https://github.com/OWNER/REPO/pull/NUMBER
` + "```" + `

The item waits until someone else posts on the pull request (a comment or a review, approvals included, bots too) or it is merged or closed. Commits, labels, and CI status do not count. The blocker then clears on its own and the item returns to the attention queue. ` + "`wi wait --clear`" + ` stops waiting early.

Only make an item wait when the user asks you to. Waiting changes the item's place in the user's attention queue. If wi reports that the agent endpoint does not allow it, give the user the exact command instead of retrying.
`

// Blocker metadata understood by this plugin.
const (
	// MetaPR names a pull request: a URL or owner/repo#number.
	MetaPR = "github.pr"
)

// Runner executes a command and returns its standard output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Plugin holds the commands used to reach wi and GitHub.
type Plugin struct {
	Wi  string
	Gh  string
	Run Runner
	Out io.Writer
	// IgnoreAuthors are logins whose comments and reviews never count, from
	// the ignore_authors plugin setting.
	IgnoreAuthors []string
	RepositoryID  string
}

// Settings are effective registered extensions.github values passed as JSON.
type Settings struct {
	IgnoreAuthors []string `json:"ignore_authors"`
}

// ParseSettings reads WI_PLUGIN_CONFIG; empty input means defaults.
func ParseSettings(value string) (Settings, error) {
	var settings Settings
	if strings.TrimSpace(value) == "" {
		return settings, nil
	}
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		return settings, fmt.Errorf("plugin config: %w", err)
	}
	return settings, nil
}

// Blocker is the subset of wi's blocker JSON this plugin reads.
type Blocker struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"`
	Event     string            `json:"event"`
	Meta      map[string]string `json:"meta"`
	CreatedAt time.Time         `json:"created_at"`
}

type item struct {
	ID           string    `json:"id"`
	RepositoryID string    `json:"repository_id"`
	Slug         string    `json:"slug"`
	Blockers     []Blocker `json:"blockers"`
}

type list struct {
	Sections map[string][]item `json:"sections"`
}

// PullRequest identifies one pull request.
type PullRequest struct {
	Owner, Repo string
	Number      int
}

func (p PullRequest) String() string {
	return fmt.Sprintf("%s/%s#%d", p.Owner, p.Repo, p.Number)
}

var (
	prURL = regexp.MustCompile(`https://github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/pull/([0-9]+)`)
	prRef = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)#([0-9]+)$`)
)

// PullRequests returns the pull request a blocker waits on, read only from
// its github.pr metadata. Event names are never parsed, so the plugin acts only
// on blockers that were explicitly tagged for it.
func PullRequests(blocker Blocker) []PullRequest {
	if blocker.Kind != "external" {
		return nil
	}
	if pr, ok := Parse(strings.TrimSpace(blocker.Meta[MetaPR])); ok {
		return []PullRequest{pr}
	}
	return nil
}

// Parse reads a pull request URL or owner/repo#number reference.
func Parse(value string) (PullRequest, bool) {
	match := prURL.FindStringSubmatch(value)
	if match == nil || match[0] != value {
		match = prRef.FindStringSubmatch(value)
	}
	if match == nil {
		return PullRequest{}, false
	}
	var number int
	if _, err := fmt.Sscanf(match[3], "%d", &number); err != nil || number <= 0 {
		return PullRequest{}, false
	}
	return PullRequest{Owner: match[1], Repo: match[2], Number: number}, true
}

// State is the pull request data the plugin needs from gh.
type State struct {
	State    string    `json:"state"`
	URL      string    `json:"url"`
	Comments []Comment `json:"comments"`
	Reviews  []Review  `json:"reviews"`
}

type Author struct {
	Login string `json:"login"`
}

type Comment struct {
	Author    Author    `json:"author"`
	CreatedAt time.Time `json:"createdAt"`
}

type Review struct {
	Author      Author    `json:"author"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submittedAt"`
}

// Authors decides whose posts count as activity: everyone, bots included,
// except the waiting user and the configured ignore list.
type Authors struct {
	Self    string
	Ignored []string
}

func normalizeLogin(login string) string {
	login = strings.ToLower(strings.TrimSpace(login))
	login = strings.TrimPrefix(login, "app/")
	return strings.TrimSuffix(login, "[bot]")
}

func (a Authors) counts(login string) bool {
	login = normalizeLogin(login)
	if login == "" || login == normalizeLogin(a.Self) {
		return false
	}
	for _, ignored := range a.Ignored {
		if login == normalizeLogin(ignored) {
			return false
		}
	}
	return true
}

// Decide reports whether a blocker is satisfied and why. Activity is a merge
// or close, or a comment or review (approvals included) posted after the item
// started waiting by anyone who counts. Commits, labels, and CI status do not.
func Decide(blocker Blocker, state State, authors Authors) (bool, string) {
	switch strings.ToUpper(state.State) {
	case "MERGED":
		return true, "merged"
	case "CLOSED":
		return true, "closed"
	}
	var first time.Time
	why := ""
	consider := func(at time.Time, login, what string) {
		if at.After(blocker.CreatedAt) && authors.counts(login) && (first.IsZero() || at.Before(first)) {
			first, why = at, what+" by "+login
		}
	}
	for _, comment := range state.Comments {
		consider(comment.CreatedAt, comment.Author.Login, "comment")
	}
	for _, review := range state.Reviews {
		if review.State == "PENDING" {
			continue
		}
		consider(review.SubmittedAt, review.Author.Login, strings.ToLower(strings.ReplaceAll(review.State, "_", " "))+" review")
	}
	return why != "", why
}

// Sync checks every open blocker that references a pull request once and
// clears the satisfied ones. Errors for one pull request do not stop others.
func (p Plugin) Sync(ctx context.Context) error {
	// "!" clears configured label filters so no waiting item is skipped.
	output, err := p.Run(ctx, p.Wi, "--json", "list", "--no-agent", "--label", "!")
	if err != nil {
		return fmt.Errorf("list work items: %w", err)
	}
	var listed list
	if err := json.Unmarshal(output, &listed); err != nil {
		return fmt.Errorf("decode wi list: %w", err)
	}
	authors := Authors{Ignored: p.IgnoreAuthors}
	login, err := p.Run(ctx, p.Gh, "api", "user", "--jq", ".login")
	if err != nil {
		return fmt.Errorf("identify the GitHub user before clearing blockers: %w", err)
	}
	authors.Self = strings.TrimSpace(string(login))
	if authors.Self == "" {
		return fmt.Errorf("identify the GitHub user before clearing blockers: empty login")
	}
	states := map[PullRequest]*State{}
	failures := 0
	checked := 0
	for _, section := range []string{"active", "waiting", "backlog"} {
		for _, entry := range listed.Sections[section] {
			if p.RepositoryID != "" && entry.RepositoryID != p.RepositoryID {
				continue
			}
			for _, blocker := range entry.Blockers {
				for _, pr := range PullRequests(blocker) {
					state, ok := states[pr]
					if !ok {
						state, err = p.view(ctx, pr)
						if err != nil {
							fmt.Fprintf(p.Out, "%s: %s: %v\n", entry.Slug, pr, err)
							failures++
						}
						states[pr] = state
					}
					if state == nil {
						continue
					}
					checked++
					done, why := Decide(blocker, *state, authors)
					if !done {
						continue
					}
					if _, err := p.Run(ctx, p.Wi, "blocker", "clear", "--id", blocker.ID, entry.ID); err != nil {
						fmt.Fprintf(p.Out, "%s: could not clear blocker %s: %v\n", entry.Slug, blocker.ID, err)
						failures++
						continue
					}
					fmt.Fprintf(p.Out, "%s: cleared blocker %s: %s %s\n", entry.Slug, blocker.ID, pr, why)
					break
				}
			}
		}
	}
	fmt.Fprintf(p.Out, "checked %d pull request blocker(s) across %d pull request(s)\n", checked, len(states))
	if failures > 0 {
		return fmt.Errorf("%d pull request check(s) failed", failures)
	}
	return nil
}

func (p Plugin) view(ctx context.Context, pr PullRequest) (*State, error) {
	output, err := p.Run(ctx, p.Gh, "pr", "view", fmt.Sprint(pr.Number), "--repo", pr.Owner+"/"+pr.Repo, "--json", "state,url,comments,reviews")
	if err != nil {
		return nil, err
	}
	var state State
	if err := json.Unmarshal(output, &state); err != nil {
		return nil, fmt.Errorf("decode gh output: %w", err)
	}
	return &state, nil
}

// Wait makes an item wait on a pull request: it checks that the pull request
// exists and is open, then adds one external blocker tagged with github.pr.
func (p Plugin) Wait(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("wi github wait", flag.ContinueOnError)
	fs.SetOutput(p.Out)
	item := fs.String("item", "", "work item selector (default: current item)")
	jsonOut := fs.Bool("json", false, "print the wi result as JSON")
	ref, rest := "", args
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		ref, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if ref == "" && fs.NArg() == 1 {
		ref = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return fmt.Errorf("usage: wi github wait <pull request URL | owner/repo#number> [--item <selector>]")
	}
	pr, ok := Parse(strings.TrimSpace(ref))
	if !ok {
		return fmt.Errorf("usage: wi github wait <pull request URL | owner/repo#number> [--item <selector>]")
	}
	state, err := p.view(ctx, pr)
	if err != nil {
		return fmt.Errorf("check %s: %w", pr, err)
	}
	if !strings.EqualFold(state.State, "OPEN") {
		return fmt.Errorf("%s is already %s", pr, strings.ToLower(state.State))
	}
	command := []string{}
	if *jsonOut {
		command = append(command, "--json")
	}
	command = append(command, "blocker", "add", "--kind", "external", "--event", "activity on "+pr.String(), "--label", fmt.Sprintf("#%d", pr.Number), "--meta", MetaPR+"="+pr.String())
	if *item != "" {
		command = append(command, "--item", *item)
	}
	output, err := p.Run(ctx, p.Wi, command...)
	if err != nil {
		return err
	}
	_, err = p.Out.Write(output)
	return err
}
