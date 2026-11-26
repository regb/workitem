package daemonapi_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/credential"
)

// A repository is registered, renamed, archived and restored, and holds the
// settings, checkout setup, environment and files its checkouts get.
func TestRepositoriesRegistryAndConfiguration(t *testing.T) {
	t.Parallel()
	f := apitest.New(t)
	repository := f.Repository(t, "test")
	clients := f.Clients

	t.Run("registry", func(t *testing.T) {
		listed := call(t, clients.Repositories.ListRepositories, &v1.ListRepositoriesRequest{}).Repositories
		if len(listed) != 1 || listed[0].Id != repository || listed[0].Name != "test" || listed[0].IntegrationBranch != "main" {
			t.Fatalf("listed: %v", listed)
		}
		renamed := call(t, clients.Repositories.RenameRepository, &v1.RenameRepositoryRequest{RepositoryId: repository, Name: "renamed"}).Repository
		if renamed.Id != repository || renamed.Name != "renamed" {
			t.Fatalf("renamed: %v", renamed)
		}
		if got := call(t, clients.Repositories.GetRepository, &v1.GetRepositoryRequest{RepositoryId: repository}).Repository; got.Name != "renamed" {
			t.Fatalf("get after rename: %v", got)
		}
		if archived := call(t, clients.Repositories.ArchiveRepository, &v1.ArchiveRepositoryRequest{RepositoryId: repository}).Repository; !archived.Archived {
			t.Fatalf("archived: %v", archived)
		}
		if listed := call(t, clients.Repositories.ListRepositories, &v1.ListRepositoriesRequest{}).Repositories; len(listed) != 0 {
			t.Fatalf("an archived repository is listed by default: %v", listed)
		}
		if listed := call(t, clients.Repositories.ListRepositories, &v1.ListRepositoriesRequest{IncludeArchived: true}).Repositories; len(listed) != 1 || !listed[0].Archived {
			t.Fatalf("archived repositories on request: %v", listed)
		}
		if restored := call(t, clients.Repositories.RestoreRepository, &v1.RestoreRepositoryRequest{RepositoryId: repository}).Repository; restored.Archived {
			t.Fatalf("restored: %v", restored)
		}
		refused(t, connect.CodeNotFound, clients.Repositories.GetRepository, &v1.GetRepositoryRequest{RepositoryId: "00000000000000000000000000"})
		refused(t, connect.CodeNotFound, clients.Repositories.GetRepositorySettings, &v1.GetRepositorySettingsRequest{RepositoryId: "00000000000000000000000000"})
	})

	t.Run("an unreachable upstream falls back to the local base with a warning", func(t *testing.T) {
		local := newItem(t, f, repository, "local base")
		status := call(t, clients.Repositories.GetRepositoryStatus, &v1.GetRepositoryStatusRequest{RepositoryId: repository})
		if status.IntegrationCommit != local.CreatedFromCommit || status.Ahead != 0 || status.Behind != 0 || status.Diverged {
			t.Fatalf("status of a local repository: %v, item from %s", status, local.CreatedFromCommit)
		}
		missing := filepath.Join(f.Root, "missing-upstream.git")
		set := call(t, clients.Repositories.SetUpstream, &v1.SetUpstreamRequest{RepositoryId: repository, Url: missing, Branch: "main"}).Repository
		got := call(t, clients.Repositories.GetRepository, &v1.GetRepositoryRequest{RepositoryId: repository}).Repository
		for name, value := range map[string]*v1.Repository{"SetUpstream": set, "GetRepository": got} {
			if value.Upstream.GetUrl() != missing || value.Upstream.GetBranch() != "main" {
				t.Fatalf("%s's upstream: %v", name, value)
			}
		}
		listed := call(t, clients.Repositories.ListRepositories, &v1.ListRepositoriesRequest{}).Repositories
		if len(listed) != 1 || listed[0].Upstream.GetUrl() != missing || listed[0].Upstream.GetBranch() != "main" {
			t.Fatalf("upstream: %v", listed)
		}
		fallback := call(t, clients.Items.CreateItem, &v1.CreateItemRequest{RepositoryId: repository, Title: "offline fallback"})
		if fallback.Item.CreatedFromCommit != local.CreatedFromCommit || len(fallback.Warnings) == 0 {
			t.Fatalf("fallback: %v", fallback)
		}
	})

	t.Run("settings are fenced by one revision", func(t *testing.T) {
		const key = "plugins.setup.run"
		// Two writers at the same revision: one wins, the other is stale.
		var group sync.WaitGroup
		codes := make(chan connect.Code, 2)
		for _, run := range []string{"first", "second"} {
			group.Add(1)
			go func() {
				defer group.Done()
				_, err := clients.Repositories.SetRepositorySetting(t.Context(), connect.NewRequest(&v1.SetRepositorySettingRequest{RepositoryId: repository, Key: key, Value: &v1.SettingValue{Value: &v1.SettingValue_StringValue{StringValue: run}}}))
				if err == nil {
					codes <- 0
				} else {
					codes <- connect.CodeOf(err)
				}
			}()
		}
		group.Wait()
		close(codes)
		var got []connect.Code
		for code := range codes {
			got = append(got, code)
		}
		slices.Sort(got)
		if !slices.Equal(got, []connect.Code{0, connect.CodeAborted}) {
			t.Fatalf("concurrent writers: %v", got)
		}
		settings := call(t, clients.Repositories.GetRepositorySettings, &v1.GetRepositorySettingsRequest{RepositoryId: repository})
		row := reposSetting(settings, key)
		if row == nil || row.Origin != "repository" || (row.Effective.GetStringValue() != "first" && row.Effective.GetStringValue() != "second") {
			t.Fatalf("setting after the race: %v", row)
		}
		refused(t, connect.CodeAborted, clients.Repositories.UnsetRepositorySetting, &v1.UnsetRepositorySettingRequest{RepositoryId: repository, Key: key, ExpectedRevision: settings.Revision + 1})
		refused(t, connect.CodeInvalidArgument, clients.Repositories.SetRepositorySetting, &v1.SetRepositorySettingRequest{RepositoryId: repository, Key: "not-a-plugin-key", Value: &v1.SettingValue{Value: &v1.SettingValue_StringValue{StringValue: "x"}}, ExpectedRevision: settings.Revision})
		unset := call(t, clients.Repositories.UnsetRepositorySetting, &v1.UnsetRepositorySettingRequest{RepositoryId: repository, Key: key, ExpectedRevision: settings.Revision})
		after := call(t, clients.Repositories.GetRepositorySettings, &v1.GetRepositorySettingsRequest{RepositoryId: repository})
		if after.Revision != unset.Revision || unset.Revision <= settings.Revision || reposSetting(after, key).GetOrigin() == "repository" {
			t.Fatalf("after unset: %v (unset at %d)", reposSetting(after, key), unset.Revision)
		}
	})

	t.Run("checkout setup", func(t *testing.T) {
		before := call(t, clients.Repositories.GetCheckoutSetup, &v1.GetCheckoutSetupRequest{RepositoryId: repository})
		// An invalid setup writes none of its fields.
		refused(t, connect.CodeInvalidArgument, clients.Repositories.UpdateCheckoutSetup, &v1.UpdateCheckoutSetupRequest{RepositoryId: repository, ExpectedRevision: before.Revision, Setup: &v1.CheckoutSetup{Run: "must not be persisted", Trust: 99}})
		if after := call(t, clients.Repositories.GetCheckoutSetup, &v1.GetCheckoutSetupRequest{RepositoryId: repository}); after.Revision != before.Revision || after.Setup.Run != before.Setup.Run {
			t.Fatalf("an invalid setup was partly written: %v", after)
		}
		updated := call(t, clients.Repositories.UpdateCheckoutSetup, &v1.UpdateCheckoutSetupRequest{RepositoryId: repository, ExpectedRevision: before.Revision, Setup: &v1.CheckoutSetup{Run: "make setup", Trust: v1.SetupTrust_SETUP_TRUST_ALWAYS, TimeoutSeconds: 30}})
		if updated.Revision == before.Revision || updated.Setup.Run != "make setup" || updated.Setup.Trust != v1.SetupTrust_SETUP_TRUST_ALWAYS {
			t.Fatalf("updated setup: %v", updated)
		}
		if got := call(t, clients.Repositories.GetCheckoutSetup, &v1.GetCheckoutSetupRequest{RepositoryId: repository}); got.Setup.Run != "make setup" || got.Revision != updated.Revision {
			t.Fatalf("setup read back: %v", got)
		}
		refused(t, connect.CodeAborted, clients.Repositories.UpdateCheckoutSetup, &v1.UpdateCheckoutSetupRequest{RepositoryId: repository, ExpectedRevision: before.Revision, Setup: &v1.CheckoutSetup{Run: "stale"}})
	})

	t.Run("environment", func(t *testing.T) {
		empty := call(t, clients.Repositories.GetEnvironment, &v1.GetEnvironmentRequest{RepositoryId: repository})
		set := call(t, clients.Repositories.UpdateEnvironment, &v1.UpdateEnvironmentRequest{RepositoryId: repository, ExpectedRevision: empty.Revision, Set: []*v1.EnvironmentVariable{{Key: "PORT", Value: "8080"}, {Key: "TOKEN", Value: "private"}}})
		if len(set.Variables) != 2 || set.Revision == empty.Revision {
			t.Fatalf("set: %v", set)
		}
		refused(t, connect.CodeAborted, clients.Repositories.UpdateEnvironment, &v1.UpdateEnvironmentRequest{RepositoryId: repository, ExpectedRevision: empty.Revision, Unset: []string{"PORT"}})
		unset := call(t, clients.Repositories.UpdateEnvironment, &v1.UpdateEnvironmentRequest{RepositoryId: repository, ExpectedRevision: set.Revision, Unset: []string{"TOKEN"}})
		got := call(t, clients.Repositories.GetEnvironment, &v1.GetEnvironmentRequest{RepositoryId: repository})
		if got.Revision != unset.Revision || len(got.Variables) != 1 || got.Variables[0].Key != "PORT" || got.Variables[0].Value != "8080" {
			t.Fatalf("environment: %v", got)
		}
	})

	t.Run("files", func(t *testing.T) {
		put := call(t, clients.Repositories.PutRepositoryFile, &v1.PutRepositoryFileRequest{RepositoryId: repository, Path: ".env", Content: []byte("PORT=8080")})
		if put.File.Path != ".env" || put.File.Size != 9 || put.File.Sha256 == "" {
			t.Fatalf("put: %v", put)
		}
		if listed := call(t, clients.Repositories.ListRepositoryFiles, &v1.ListRepositoryFilesRequest{RepositoryId: repository}).Files; len(listed) != 1 || listed[0].Sha256 != put.File.Sha256 {
			t.Fatalf("listed: %v", listed)
		}
		if read := call(t, clients.Repositories.ReadRepositoryFile, &v1.ReadRepositoryFileRequest{RepositoryId: repository, Path: ".env"}); string(read.Content) != "PORT=8080" {
			t.Fatalf("read: %q", read.Content)
		}
		refused(t, connect.CodeFailedPrecondition, clients.Repositories.PutRepositoryFile, &v1.PutRepositoryFileRequest{RepositoryId: repository, Path: "../escape", Content: []byte("x")})
		call(t, clients.Repositories.DeleteRepositoryFile, &v1.DeleteRepositoryFileRequest{RepositoryId: repository, Path: ".env"})
		if listed := call(t, clients.Repositories.ListRepositoryFiles, &v1.ListRepositoryFilesRequest{RepositoryId: repository}).Files; len(listed) != 0 {
			t.Fatalf("listed after delete: %v", listed)
		}
		refused(t, connect.CodeNotFound, clients.Repositories.ReadRepositoryFile, &v1.ReadRepositoryFileRequest{RepositoryId: repository, Path: ".env"})
		refused(t, connect.CodeNotFound, clients.Repositories.DeleteRepositoryFile, &v1.DeleteRepositoryFileRequest{RepositoryId: repository, Path: ".env"})
	})

	// A base relative to a checkout means nothing in the bare store: the
	// client resolves it in its own checkout and sends a commit.
	t.Run("a contextual base is refused", func(t *testing.T) {
		for _, base := range []string{"HEAD", "HEAD~1", "@", "@{-1}", "-", "main@{0}"} {
			refused(t, connect.CodeFailedPrecondition, clients.Items.CreateItem, &v1.CreateItemRequest{RepositoryId: repository, Title: "contextual " + base, Base: base})
		}
		integration := call(t, clients.Items.CreateItem, &v1.CreateItemRequest{RepositoryId: repository, Title: "integration base", Base: "^"}).Item
		if latest := newItem(t, f, repository, "default base"); integration.CreatedFromCommit != latest.CreatedFromCommit {
			t.Fatalf("^ means the integration branch: %s, want %s", integration.CreatedFromCommit, latest.CreatedFromCommit)
		}
	})
}

func reposSetting(settings *v1.GetRepositorySettingsResponse, key string) *v1.RepositorySetting {
	for _, row := range settings.Settings {
		if row.Key == key {
			return row
		}
	}
	return nil
}

// A sync's job says what it did: whether the branch moved, and to what; the
// repository's status shows it behind its upstream until then.
func TestRepositoriesSyncReportsWhereItLeftTheRepository(t *testing.T) {
	t.Parallel()
	f := apitest.New(t)
	upstream := filepath.Join(f.Root, "upstream")
	git(t, f.Root, "init", "-b", "main", upstream)
	git(t, upstream, "commit", "--allow-empty", "-m", "initial")
	added := call(t, f.Clients.Repositories.AddRepository, &v1.AddRepositoryRequest{Name: "synced", Source: upstream, Branch: "main", UpstreamUrl: upstream, UpstreamBranch: "main"})
	repository := apitest.Wait(t, f.Clients, added.Job).RepositoryId
	git(t, upstream, "commit", "--allow-empty", "-m", "upstream moved")
	tip := git(t, upstream, "rev-parse", "HEAD")
	if status := call(t, f.Clients.Repositories.GetRepositoryStatus, &v1.GetRepositoryStatusRequest{RepositoryId: repository, Fetch: true}); status.UpstreamCommit != tip || status.Behind != 1 || status.Ahead != 0 || status.FetchedAt == nil {
		t.Fatalf("status before sync: %v, upstream at %s", status, tip)
	}
	synchronize := func() *v1.RepositorySyncResult {
		t.Helper()
		started := call(t, f.Clients.Repositories.SyncRepository, &v1.SyncRepositoryRequest{RepositoryId: repository, Mode: v1.SyncMode_SYNC_MODE_FAST_FORWARD})
		job := apitest.Wait(t, f.Clients, started.Job)
		if job.GetRepositorySync() == nil {
			t.Fatalf("sync job: %v", job)
		}
		return job.GetRepositorySync()
	}
	if result := synchronize(); !result.Changed || result.LocalSha != tip || result.UpstreamSha != tip {
		t.Fatalf("first sync: %v, upstream at %s", result, tip)
	}
	if result := synchronize(); result.Changed || result.LocalSha != tip {
		t.Fatalf("second sync: %v", result)
	}
	if status := call(t, f.Clients.Repositories.GetRepositoryStatus, &v1.GetRepositoryStatusRequest{RepositoryId: repository}); status.IntegrationCommit != tip || status.Behind != 0 {
		t.Fatalf("status after sync: %v", status)
	}
	refused(t, connect.CodeFailedPrecondition, f.Clients.Repositories.SyncRepository, &v1.SyncRepositoryRequest{RepositoryId: repository, Mode: v1.SyncMode_SYNC_MODE_RESET_TO_UPSTREAM})
}

// Integrating an item squashes its work onto the target branch by default,
// or preserves its series with rebase, and records every merge.
func TestRepositoriesIntegrateRecordsSquashAndPreservedSeries(t *testing.T) {
	t.Parallel()
	f := apitest.New(t)
	repository := f.Repository(t, "test")
	item, checkout := activeItem(t, f, repository, "Default squash title")
	path := checkout.Claim.Path
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(path, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := git(t, path, "rev-parse", "main")
	write("work.txt", "uncommitted merge input\n")
	// Unresolved review feedback warns but does not block the merge.
	input := call(t, f.Clients.Reviews.CaptureReviewRevision, &v1.CaptureReviewRevisionRequest{ItemId: item.Id, CaptureId: "feedback", ExpectedClaimId: checkout.Claim.Id, ExpectedClaimGeneration: checkout.Claim.Generation}).Revision
	draft := call(t, f.Clients.Reviews.SaveReviewDraft, &v1.SaveReviewDraftRequest{ItemId: item.Id, OperationId: "draft", Draft: &v1.ReviewDraft{Anchor: &v1.ReviewAnchor{RevisionId: input.Id}, Body: "An unresolved concern"}}).Result
	// Without an agent to notify, publishing succeeds and says why it did
	// not tell the agent.
	if published := call(t, f.Clients.Reviews.PublishReviewRound, &v1.PublishReviewRoundRequest{ItemId: item.Id, OperationId: "publish", RevisionId: input.Id, DraftIds: []string{draft.DraftId}}); len(published.Warnings) != 1 || !strings.Contains(published.Warnings[0], "is published, but its agent was not told") {
		t.Fatalf("publish: %v", published)
	}
	integrate := func(request *v1.IntegrateItemRequest) *v1.Job {
		t.Helper()
		request.ItemId = item.Id
		return apitest.Wait(t, f.Clients, call(t, f.Clients.Repositories.IntegrateItem, request).Job)
	}
	job := integrate(&v1.IntegrateItemRequest{Local: true, Message: "Reviewed squash"})
	if len(job.Warnings) == 0 {
		t.Fatalf("unresolved feedback was not reported: %v", job)
	}
	after := git(t, path, "rev-parse", "main")
	if merged := job.GetIntegration(); merged.GetCommit() != after || merged.GetTargetBranch() != "main" || merged.GetRevisionId() == "" {
		t.Fatalf("merge result: %v", job)
	}
	if git(t, path, "rev-parse", "HEAD") != after || git(t, path, "rev-parse", after+"^") != before || git(t, path, "log", "-1", "--format=%s", after) != "Reviewed squash" {
		t.Fatal("wrong squash, or the checkout did not continue from it")
	}
	records := call(t, f.Clients.Reviews.ListReviewRevisions, &v1.ListReviewRevisionsRequest{ItemId: item.Id})
	if len(records.Merges) != 1 || records.Merges[0].Commit != after || records.Merges[0].Before != before || records.Merges[0].Tree != git(t, path, "rev-parse", "HEAD^{tree}") {
		t.Fatalf("merge record: %v", records.Merges)
	}
	if found := call(t, f.Clients.Repositories.SearchRepositoryFiles, &v1.SearchRepositoryFilesRequest{RepositoryId: repository, Query: "work"}).Paths; !slices.Contains(found, "work.txt") {
		t.Fatalf("the integration branch's files: %v", found)
	}

	for _, name := range []string{"one.txt", "two.txt"} {
		write(name, name)
		git(t, path, "add", name)
		git(t, path, "commit", "-m", name)
	}
	tip := git(t, path, "rev-parse", "HEAD")
	if job := integrate(&v1.IntegrateItemRequest{TargetBranch: "main", Local: true, Rebase: true}); job.GetIntegration().GetCommit() != tip {
		t.Fatalf("rebase result: %v", job)
	}
	if git(t, path, "rev-parse", "main") != tip || git(t, path, "rev-list", "--count", after+"..main") != "2" {
		t.Fatal("rebase did not preserve the series")
	}
	integrate(&v1.IntegrateItemRequest{Local: true})
	if git(t, path, "rev-parse", "main") != tip {
		t.Fatal("already contained work made an empty commit")
	}
	if found := call(t, f.Clients.Repositories.SearchRepositoryFiles, &v1.SearchRepositoryFilesRequest{RepositoryId: repository, Query: "txt", Limit: 2}).Paths; len(found) != 2 {
		t.Fatalf("a limited search: %v", found)
	}
	if records := call(t, f.Clients.Reviews.ListReviewRevisions, &v1.ListReviewRevisionsRequest{ItemId: item.Id}); len(records.Merges) != 3 {
		t.Fatalf("merge history: %v", records.Merges)
	}
}

// A merge leaves an idle agent running, refuses to merge under a running
// turn, except for the item's own agent, which merges from inside its turn.
func TestRepositoriesIntegrateBesideTheItemsAgent(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.WithAgents())
	item, agent := startedAgent(t, f)
	eventually(t, "settlement capture", func() bool {
		return !call(t, f.Clients.Items.GetItem, &v1.GetItemRequest{ItemId: item}).GetObservation().GetCapturing()
	})
	path := call(t, f.Clients.Workspaces.GetWorkspace, &v1.GetWorkspaceRequest{ItemId: item}).Checkout.Claim.Path
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(path, name), []byte(name+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("idle-work.txt")
	apitest.Wait(t, f.Clients, call(t, f.Clients.Repositories.IntegrateItem, &v1.IntegrateItemRequest{ItemId: item, Local: true}).Job)
	if current := apitest.WaitIdle(t, f.Clients, item); current.Generation != agent.Generation {
		t.Fatalf("merge replaced the idle agent: %d -> %d", agent.Generation, current.Generation)
	}

	call(t, f.Clients.Agents.SendMessage, &v1.SendMessageRequest{ItemId: item, Text: "hold the turn", Delivery: v1.Delivery_DELIVERY_PROMPT})
	eventually(t, "a running turn", func() bool {
		return call(t, f.Clients.Agents.GetAgent, &v1.GetAgentRequest{ItemId: item}).Agent.Status == v1.AgentStatus_AGENT_STATUS_BUSY
	})
	if job := reposFinished(t, f, call(t, f.Clients.Repositories.IntegrateItem, &v1.IntegrateItemRequest{ItemId: item, Local: true}).Job); job.State != v1.JobState_JOB_STATE_FAILED || !strings.Contains(job.Error, "busy running a turn") {
		t.Fatalf("a merge under a running turn: %v", job)
	}
	forged := f.ClientsWith(t, func() (string, error) { return "wi_item_" + item + "_" + strings.Repeat("A", 43), nil })
	refused(t, connect.CodeUnauthenticated, forged.Repositories.IntegrateItem, &v1.IntegrateItemRequest{ItemId: item, Local: true})
	// The runtime's credential, in the file its environment names.
	files, _ := filepath.Glob(filepath.Join(f.Paths.DataStateRoot, "items", item, "runtimes", "*", "credential"))
	if len(files) != 1 {
		t.Fatalf("the runtime's credential: %v", files)
	}
	own := f.ClientsWith(t, credential.Source(files[0], credential.Item))
	write("agent-work.txt")
	apitest.Wait(t, own, call(t, own.Repositories.IntegrateItem, &v1.IntegrateItemRequest{ItemId: item, Local: true}).Job)
	if !strings.Contains(git(t, path, "show", "--stat", "main"), "agent-work.txt") {
		t.Fatal("the agent's own merge did not land")
	}
}

// reposFinished watches job until it ends, and returns it however it ended.
func reposFinished(t *testing.T, f *apitest.Fixture, job *v1.Job) *v1.Job {
	t.Helper()
	stream, err := f.Clients.Jobs.WatchJob(t.Context(), connect.NewRequest(&v1.WatchJobRequest{JobId: job.Id}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for stream.Receive() {
		job = stream.Msg().Job
	}
	if stream.Err() != nil {
		t.Fatal(stream.Err())
	}
	return job
}
