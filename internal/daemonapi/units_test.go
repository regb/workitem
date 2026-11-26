package daemonapi

// Pure units of the server: attention, list sections, delegations at work,
// error codes, file search ranking, quota availability and repository
// onboarding.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apperr"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestAWorkingAgentNeedsNoAttentionForItsLastResponse(t *testing.T) {
	busy, idle, problem := v1.AgentStatus_AGENT_STATUS_BUSY, v1.AgentStatus_AGENT_STATUS_IDLE, v1.AgentStatus_AGENT_STATUS_PROBLEM
	for _, c := range []struct {
		name    string
		o       *v1.ItemObservation
		waiting bool
		want    bool
	}{
		{"unread reply while the agent works again", &v1.ItemObservation{AgentStatus: busy, Unread: true}, false, false},
		{"unread reply on an idle agent", &v1.ItemObservation{AgentStatus: idle, Unread: true}, false, true},
		{"idle agent waiting for input", &v1.ItemObservation{AgentStatus: idle}, false, true},
		{"idle agent while capturing", &v1.ItemObservation{AgentStatus: idle, Capturing: true}, false, false},
		{"unread reply while capturing", &v1.ItemObservation{AgentStatus: idle, Unread: true, Capturing: true}, false, false},
		{"question while capturing", &v1.ItemObservation{AgentStatus: idle, Capturing: true, Asking: true}, false, true},
		{"idle agent waiting on background work", &v1.ItemObservation{AgentStatus: idle, BackgroundTasks: 2}, false, false},
		{"unread reply while background work runs", &v1.ItemObservation{AgentStatus: idle, Unread: true, BackgroundTasks: 1}, false, false},
		{"idle agent waiting on working delegations", &v1.ItemObservation{AgentStatus: idle, WorkingDelegations: 2}, false, false},
		{"unread reply while a delegation works", &v1.ItemObservation{AgentStatus: idle, Unread: true, WorkingDelegations: 1}, false, false},
		{"question while a delegation works", &v1.ItemObservation{AgentStatus: idle, Asking: true, WorkingDelegations: 1}, false, true},
		{"question while busy", &v1.ItemObservation{AgentStatus: busy, Asking: true}, false, true},
		{"problem", &v1.ItemObservation{AgentStatus: problem, Unread: true}, false, true},
		{"waiting item with an unread reply", &v1.ItemObservation{AgentStatus: idle, Unread: true}, true, true},
		{"waiting item, idle agent", &v1.ItemObservation{AgentStatus: idle}, true, false},
		{"busy agent on another branch with changes", &v1.ItemObservation{AgentStatus: busy, BranchMismatch: true, WorktreeStatus: v1.WorktreeStatus_WORKTREE_STATUS_PROBLEM, WorktreeDirty: true}, false, false},
		{"branch mismatch while background work runs", &v1.ItemObservation{AgentStatus: idle, BranchMismatch: true, BackgroundTasks: 1}, false, false},
		{"settled agent on another branch", &v1.ItemObservation{AgentStatus: idle, BranchMismatch: true}, false, true},
		{"checkout problem with no agent running", &v1.ItemObservation{WorktreeStatus: v1.WorktreeStatus_WORKTREE_STATUS_PROBLEM}, false, true},
	} {
		if got := needsAttention(c.o, c.waiting); got != c.want {
			t.Errorf("%s: needsAttention=%v, want %v", c.name, got, c.want)
		}
	}
}

func TestADelegationWorksWhileItsAgentDoesOrWaitsOnWork(t *testing.T) {
	busy, idle := v1.AgentStatus_AGENT_STATUS_BUSY, v1.AgentStatus_AGENT_STATUS_IDLE
	for _, c := range []struct {
		name string
		o    *v1.ItemObservation
		want bool
	}{
		{"busy", &v1.ItemObservation{AgentStatus: busy}, true},
		{"asking", &v1.ItemObservation{AgentStatus: busy, Asking: true}, false},
		{"idle", &v1.ItemObservation{AgentStatus: idle, Unread: true}, false},
		{"waiting on background work", &v1.ItemObservation{AgentStatus: idle, BackgroundTasks: 1}, true},
		{"waiting on its own delegations", &v1.ItemObservation{AgentStatus: idle, WorkingDelegations: 1}, true},
		{"problem", &v1.ItemObservation{AgentStatus: v1.AgentStatus_AGENT_STATUS_PROBLEM}, false},
	} {
		if got := working(c.o); got != c.want {
			t.Errorf("%s: working=%v, want %v", c.name, got, c.want)
		}
	}
}

func TestListSectionFollowsAttentionThenProblems(t *testing.T) {
	problem := &v1.ItemObservation{AgentStatus: v1.AgentStatus_AGENT_STATUS_PROBLEM}
	checkout := &v1.ItemObservation{WorktreeStatus: v1.WorktreeStatus_WORKTREE_STATUS_PROBLEM}
	for _, test := range []struct {
		group v1.AttentionGroup
		o     *v1.ItemObservation
		want  v1.ListSection
	}{
		{v1.AttentionGroup_ATTENTION_GROUP_CLOSED, problem, v1.ListSection_LIST_SECTION_CLOSED},
		{v1.AttentionGroup_ATTENTION_GROUP_BACKLOG, problem, v1.ListSection_LIST_SECTION_BACKLOG},
		// A usage-limited agent is a problem, but its item waits.
		{v1.AttentionGroup_ATTENTION_GROUP_WAITING, problem, v1.ListSection_LIST_SECTION_WAITING},
		{v1.AttentionGroup_ATTENTION_GROUP_WORKING, checkout, v1.ListSection_LIST_SECTION_NEEDS_FIXING},
		{v1.AttentionGroup_ATTENTION_GROUP_NEEDS_ATTENTION, problem, v1.ListSection_LIST_SECTION_NEEDS_FIXING},
		{v1.AttentionGroup_ATTENTION_GROUP_WORKING, &v1.ItemObservation{}, v1.ListSection_LIST_SECTION_IN_PROGRESS},
		{v1.AttentionGroup_ATTENTION_GROUP_NEEDS_ATTENTION, nil, v1.ListSection_LIST_SECTION_NEEDS_ATTENTION},
	} {
		if got := listSection(&v1.ItemSummary{AttentionGroup: test.group, Observation: test.o}); got != test.want {
			t.Errorf("%v %v: got %v, want %v", test.group, test.o, got, test.want)
		}
	}
}

// An error's kind decides its code, also across the coordinator protocol;
// its wording no longer can make a malformed request look stale.
func TestErrorCodesComeFromKinds(t *testing.T) {
	for _, c := range []struct {
		err  error
		want connect.Code
	}{
		{apperr.Invalidf("workspace claim id and generation must either both be set or both be absent"), connect.CodeInvalidArgument},
		{fmt.Errorf("start: %w", apperr.Stalef("workspace claim generation 3 does not follow 2")), connect.CodeAborted},
		{&coordinator.ProtocolError{Code: "command_error", Message: "the schedule moved on", Kind: "stale"}, connect.CodeAborted},
		{&coordinator.ProtocolError{Code: "command_error", Message: "work item not found", Kind: "not_found"}, connect.CodeNotFound},
		{apperr.Exhaustedf("agent generation exhausted"), connect.CodeResourceExhausted},
		{errors.New("a runtime of another generation is starting"), connect.CodeFailedPrecondition},
		{errors.New("item revision conflict: expected 2, current 3"), connect.CodeAborted},
	} {
		if got := connect.CodeOf(rpcError(c.err)); got != c.want {
			t.Errorf("%q: %v, want %v", c.err, got, c.want)
		}
	}
}

// A partial outcome lists only steps that completed: an operation that did
// nothing reports a plain error, and warnings never count as steps.
func TestPartialOutcomesListOnlyCompletedSteps(t *testing.T) {
	var ce *connect.Error
	if errors.As(partial(apperr.Stalef("revision moved"), "item"), &ce) && len(ce.Details()) != 0 {
		t.Fatalf("nothing completed, yet a partial outcome: %v", ce.Details())
	}
	if !errors.As(partial(errors.New("setup failed"), "item", "claimed workspace"), &ce) || len(ce.Details()) != 1 {
		t.Fatal("completed step not reported")
	}
	err := withWarnings(apperr.Stalef("agent busy"), []string{"terminal attached"})
	if !errors.Is(err, apperr.Stale) || err.Error() != "agent busy (terminal attached)" || withWarnings(err, nil) != err {
		t.Fatalf("%v", err)
	}
}

func TestMentionFilesRankWorktreePathsWithFzf(t *testing.T) {
	if _, err := exec.LookPath("fzf"); err != nil {
		t.Skip("fzf is not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	for name, body := range map[string]string{"docs/readme.md": "docs", "internal/web/server.go": "package web", ".gitignore": "build/\n", "build/out.bin": "binary", "notes.txt": "untracked"} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("git", "-C", dir, "add", "docs", "internal", ".gitignore").CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	ctx := context.Background()
	found, err := findFiles(ctx, dir, "readme")
	if err != nil || len(found) == 0 || found[0] != "docs/readme.md" {
		t.Fatalf("readme: %v %v", found, err)
	}
	if found, _ := findFiles(ctx, dir, "intweb"); !slices.Contains(found, "internal/web/") || !slices.Contains(found, "internal/web/server.go") {
		t.Fatalf("folders and fuzzy match: %v", found)
	}
	all, err := findFiles(ctx, dir, "")
	if err != nil || slices.Contains(all, "build/out.bin") || !slices.Contains(all, "notes.txt") {
		t.Fatalf("ignored or untracked files: %v %v", all, err)
	}
	// The shallowest paths come first without a query.
	if pathDepth(all[0]) != 0 {
		t.Fatalf("empty query order: %v", all)
	}
	if none, err := findFiles(ctx, dir, "zzzzqqq"); err != nil || len(none) != 0 {
		t.Fatalf("no match: %v %v", none, err)
	}
	// New work searches the committed tree of the repository instead.
	if out, err := exec.Command("git", "-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "files").CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	committed, err := findCommitFiles(ctx, filepath.Join(dir, ".git"), "HEAD", "servgo")
	if err != nil || len(committed) == 0 || committed[0] != "internal/web/server.go" || slices.Contains(committed, "notes.txt") {
		t.Fatalf("commit files: %v %v", committed, err)
	}
}

func TestQuotaAvailabilityWaitsForEveryExhaustedWindow(t *testing.T) {
	soon, later := time.Now().Add(time.Hour), time.Now().Add(24*time.Hour)
	window := func(remaining float64, reset time.Time) *v1.UsageWindow {
		return &v1.UsageWindow{RemainingPercent: proto.Float64(remaining), ResetsAt: timestamppb.New(reset)}
	}
	available, reset := quotaAvailability(&v1.UsageQuota{Windows: []*v1.UsageWindow{window(0, soon), window(0, later), window(40, soon)}})
	if available || reset == nil || !reset.Equal(later) {
		t.Fatalf("exhausted: %v %v", available, reset)
	}
	if available, reset := quotaAvailability(&v1.UsageQuota{Windows: []*v1.UsageWindow{window(10, soon), {}}}); !available || reset != nil {
		t.Fatalf("available: %v %v", available, reset)
	}
	if available, _ := quotaAvailability(&v1.UsageQuota{IncludedUsageAllowed: proto.Bool(false)}); available {
		t.Fatal("a refused subscription is available")
	}
}

func TestRemoteDefaultBranchIgnoresRepositoryEnvironment(t *testing.T) {
	repo, unrelated := t.TempDir(), t.TempDir()
	for _, entry := range []struct{ path, branch string }{{repo, "intended"}, {unrelated, "wrong"}} {
		if out, err := exec.Command("git", "init", "--bare", "-b", entry.branch, entry.path).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	// ls-remote needs an existing HEAD ref.
	seed := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "intended", seed}, {"-C", seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-m", "init"}, {"-C", seed, "push", repo, "intended"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	t.Setenv("GIT_DIR", unrelated)
	t.Setenv("GIT_WORK_TREE", filepath.Join(unrelated, "work"))
	got, err := remoteDefaultBranch(context.Background(), repo)
	if err != nil || got != "intended" {
		t.Fatalf("branch=%s err=%v", got, err)
	}
}

func TestAnOwnerCountsItsWorkingDelegations(t *testing.T) {
	busy, idle := v1.AgentStatus_AGENT_STATUS_BUSY, v1.AgentStatus_AGENT_STATUS_IDLE
	owner := "01K1OWNER0000000000000000"
	manifest := func(id, ownedBy, state string) model.Manifest {
		m := model.NewManifest(id, id, id, nil, false, time.Now(), model.Repository{}, model.Checkout{})
		m.Owner, m.State = ownedBy, state
		return m
	}
	of := model.ItemPrincipal(owner)
	manifests := []model.Manifest{
		manifest("01K1BUSY00000000000000000", of, model.StateActive),
		manifest("01K1BACKGROUND00000000000", of, model.StateActive),
		manifest("01K1ASKING000000000000000", of, model.StateActive),
		manifest("01K1IDLE00000000000000000", of, model.StateActive),
		manifest("01K1BACKLOG00000000000000", of, model.StateBacklog),
		manifest("01K1OTHER0000000000000000", model.ItemPrincipal("01K1ELSE00000000000000000"), model.StateActive),
		manifest("01K1MINE00000000000000000", model.OperatorUser, model.StateActive),
	}
	observations := map[string]*v1.ItemObservation{
		"01K1BUSY00000000000000000": {AgentStatus: busy},
		"01K1BACKGROUND00000000000": {AgentStatus: idle, BackgroundTasks: 1},
		"01K1ASKING000000000000000": {AgentStatus: busy, Asking: true},
		"01K1IDLE00000000000000000": {AgentStatus: idle, Unread: true},
		"01K1BACKLOG00000000000000": {AgentStatus: busy},
		"01K1OTHER0000000000000000": {AgentStatus: busy},
		"01K1MINE00000000000000000": {AgentStatus: busy},
	}
	if got := countWorking(manifests, owner, func(m model.Manifest) *v1.ItemObservation { return observations[m.ID] }); got != 2 {
		t.Fatalf("working delegations = %d, want 2", got)
	}
}
