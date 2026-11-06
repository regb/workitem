package github

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Generate the same protojson contract as wi list --json, not a hand-written
// presentation projection that can drift away from the CLI.
func listOutput(t *testing.T, items ...*v1.ItemSummary) []byte {
	t.Helper()
	output, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(&v1.ListItemsResponse{Items: items})
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func TestPullRequestsOnlyFromMetadata(t *testing.T) {
	for _, tc := range []struct {
		blocker Blocker
		want    []string
	}{
		{Blocker{Kind: "external", Event: "review of acme/api#9", Meta: map[string]string{MetaPR: "acme/api#9"}}, []string{"acme/api#9"}},
		{Blocker{Kind: "time", Meta: map[string]string{MetaPR: "acme/api#9"}}, nil},
		{Blocker{Kind: "item", Meta: map[string]string{MetaPR: "acme/api#9"}}, nil},
		{Blocker{Kind: "external", Meta: map[string]string{MetaPR: "https://github.com/acme/api/pull/42"}}, []string{"acme/api#42"}},
		{Blocker{Kind: "external", Event: "deploy after https://github.com/acme/api/pull/42 ships"}, nil},
		{Blocker{Kind: "external", Meta: map[string]string{MetaPR: "not a pr"}}, nil},
	} {
		got := []string{}
		for _, pr := range PullRequests(tc.blocker) {
			got = append(got, pr.String())
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("%+v: got %v want %v", tc.blocker, got, tc.want)
		}
	}
}

func TestWaitValidatesThePullRequestAndTagsTheBlocker(t *testing.T) {
	var added []string
	state := `{"state":"OPEN"}`
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "gh" {
			return []byte(state), nil
		}
		added = args
		return []byte("added\n"), nil
	}
	var out bytes.Buffer
	plugin := Plugin{Wi: "wi", Gh: "gh", Run: run, Out: &out}
	if err := plugin.Wait(context.Background(), []string{"https://github.com/acme/api/pull/42", "--item", "auth"}); err != nil {
		t.Fatal(err)
	}
	if want := "blocker add --kind external --event activity on acme/api#42 --label #42 --meta github.pr=acme/api#42 --item auth"; strings.Join(added, " ") != want {
		t.Fatalf("wi args = %q", strings.Join(added, " "))
	}
	state = `{"state":"MERGED"}`
	if err := plugin.Wait(context.Background(), []string{"acme/api#42"}); err == nil || !strings.Contains(err.Error(), "already merged") {
		t.Fatalf("merged PR accepted: %v", err)
	}
	for _, bad := range [][]string{{}, {"acme/api"}, {"acme/api#1", "--until", "merge"}} {
		if err := plugin.Wait(context.Background(), bad); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
}

func TestDecideCountsPostsByOthersAndMergeOrClose(t *testing.T) {
	created := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	blocker := Blocker{CreatedAt: created}
	authors := Authors{Self: "regb", Ignored: []string{"codecov"}}
	comment := func(login string, offset time.Duration) Comment {
		return Comment{Author: Author{Login: login}, CreatedAt: created.Add(offset)}
	}
	quiet := State{State: "OPEN", Comments: []Comment{comment("alice", -time.Hour), comment("regb", time.Hour), comment("codecov[bot]", time.Hour)}, Reviews: []Review{{Author: Author{Login: "bob"}, State: "PENDING", SubmittedAt: created.Add(time.Hour)}}}
	if done, why := Decide(blocker, quiet, authors); done {
		t.Fatalf("ignored activity cleared it: %s", why)
	}
	busy := quiet
	busy.Comments = append(busy.Comments, comment("dependabot", 2*time.Hour))
	busy.Reviews = append(busy.Reviews, Review{Author: Author{Login: "carol"}, State: "APPROVED", SubmittedAt: created.Add(90 * time.Minute)})
	if done, why := Decide(blocker, busy, authors); !done || why != "approved review by carol" {
		t.Fatalf("done=%v why=%q", done, why)
	}
	for state, want := range map[string]string{"MERGED": "merged", "CLOSED": "closed"} {
		if done, why := Decide(blocker, State{State: state}, authors); !done || why != want {
			t.Fatalf("%s: done=%v why=%q", state, done, why)
		}
	}
	if settings, err := ParseSettings(`{"ignore_authors":["codecov","vercel"]}`); err != nil || len(settings.IgnoreAuthors) != 2 {
		t.Fatalf("settings=%+v err=%v", settings, err)
	}
}

func TestSyncClearsSatisfiedBlockersAndChecksEachPullRequestOnce(t *testing.T) {
	created := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	listing := listOutput(t,
		&v1.ItemSummary{Id: "item-a", Slug: "a", AttentionGroup: v1.AttentionGroup_ATTENTION_GROUP_NEEDS_ATTENTION, Blockers: []*v1.Blocker{{Id: "b1", Kind: v1.BlockerKind_BLOCKER_KIND_EXTERNAL, Event: "review of acme/api#42", Meta: map[string]string{MetaPR: "acme/api#42"}, CreatedAt: timestamppb.New(created)}}},
		&v1.ItemSummary{Id: "item-d", Slug: "d", Blockers: []*v1.Blocker{{Id: "b3", Kind: v1.BlockerKind_BLOCKER_KIND_EXTERNAL, Event: "https://github.com/acme/api/pull/42", CreatedAt: timestamppb.New(created)}}},
		&v1.ItemSummary{Id: "item-b", Slug: "b", Blockers: []*v1.Blocker{{Id: "b2", Kind: v1.BlockerKind_BLOCKER_KIND_EXTERNAL, Meta: map[string]string{MetaPR: "acme/api#42"}, CreatedAt: timestamppb.New(created.Add(2 * time.Hour))}}},
		&v1.ItemSummary{Id: "item-c", Slug: "c"},
	)
	views, cleared := 0, []string{}
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "wi" && args[1] == "list":
			return listing, nil
		case name == "gh" && args[0] == "api":
			return []byte("regb\n"), nil
		case name == "gh":
			views++
			return []byte(`{"state":"OPEN","comments":[{"author":{"login":"regb"},"createdAt":"2026-10-16T10:30:00Z"},{"author":{"login":"bob"},"createdAt":"2026-10-16T11:00:00Z"}]}`), nil
		case name == "wi" && args[0] == "blocker":
			cleared = append(cleared, args[3]+" "+args[len(args)-1])
			return nil, nil
		}
		t.Fatalf("unexpected command %s %v", name, args)
		return nil, nil
	}
	var out bytes.Buffer
	if err := (Plugin{Wi: "wi", Gh: "gh", Run: run, Out: &out}).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if views != 1 || len(cleared) != 1 || cleared[0] != "b1 item-a" || !strings.Contains(out.String(), "acme/api#42 comment by bob") {
		t.Fatalf("views=%d cleared=%v output=%s", views, cleared, out.String())
	}
}

func TestSyncDoesNotClearBlockersWithoutKnownSelfIdentity(t *testing.T) {
	for _, failed := range []bool{false, true} {
		var out bytes.Buffer
		cleared := false
		p := Plugin{Wi: "wi", Gh: "gh", Out: &out, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "wi" && args[1] == "list" {
				return []byte(`{"items":[{"id":"item","blockers":[{"id":"b1","kind":"BLOCKER_KIND_EXTERNAL","meta":{"github.pr":"acme/api#42"}}]}]}`), nil
			}
			if name == "gh" && args[0] == "api" {
				if failed {
					return nil, fmt.Errorf("authentication unavailable")
				}
				return nil, nil
			}
			if name == "wi" && args[0] == "blocker" {
				cleared = true
			}
			return []byte(`{"state":"MERGED"}`), nil
		}}
		if err := p.Sync(context.Background()); err == nil || cleared {
			t.Fatal("missing identity silently widened clearance policy", err)
		}
	}
}

func TestSkillTeachesWaitingOnAPullRequest(t *testing.T) {
	for _, want := range []string{"wi github wait https://github.com/OWNER/REPO/pull/NUMBER", "someone else posts on the pull request", "Only make an item wait when the user asks"} {
		if !strings.Contains(Skill, want) {
			t.Fatalf("skill lacks %q", want)
		}
	}
}

func TestSyncSkipsGitHubWhenNothingInScopeWaitsOnAPullRequest(t *testing.T) {
	listing := listOutput(t,
		&v1.ItemSummary{Id: "item-a", Slug: "a", RepositoryId: "repo-1", Blockers: []*v1.Blocker{{Id: "b1", Kind: v1.BlockerKind_BLOCKER_KIND_EXTERNAL, Event: "security review"}}},
		&v1.ItemSummary{Id: "item-b", Slug: "b", RepositoryId: "repo-2", Blockers: []*v1.Blocker{{Id: "b2", Kind: v1.BlockerKind_BLOCKER_KIND_EXTERNAL, Meta: map[string]string{MetaPR: "acme/api#42"}}}},
	)
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "wi" && args[1] == "list" {
			return listing, nil
		}
		t.Fatalf("unexpected command %s %v", name, args)
		return nil, nil
	}
	var out bytes.Buffer
	if err := (Plugin{Wi: "wi", Gh: "gh", Run: run, Out: &out, RepositoryID: "repo-1"}).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "checked 0 pull request blocker(s)") {
		t.Fatalf("output = %s", out.String())
	}
}
