package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/regb/workitem/internal/agent"
	itemlock "github.com/regb/workitem/internal/lock"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/plugin"
	"github.com/regb/workitem/internal/repofiles"
	"github.com/regb/workitem/internal/store"
)

// The setup record lives in the store, where the daemon reads it too.
const (
	SetupRunning   = store.SetupRunning
	SetupSucceeded = store.SetupSucceeded
	SetupFailed    = store.SetupFailed
	SetupSkipped   = store.SetupSkipped
)

type SetupRun = store.SetupRun

type SetupResult struct {
	WorkItemID string   `json:"work_item_id"`
	Ran        bool     `json:"ran"`
	Run        SetupRun `json:"run"`
	Warnings   []string `json:"warnings"`
}

// SetupStatus is the setup of an item's current claim, with the end of its
// log, for clients that show progress.
type SetupStatus struct {
	WorkItemID string    `json:"work_item_id"`
	Current    bool      `json:"current"`
	Run        *SetupRun `json:"run,omitempty"`
	LogTail    string    `json:"log_tail,omitempty"`
}

type skipSetupKey struct{}

// WithSkipSetup starts agents under ctx without setting up a new claim
// first, recording the setup as skipped: the escape when setup itself is
// broken.
func WithSkipSetup(ctx context.Context) context.Context {
	return context.WithValue(ctx, skipSetupKey{}, true)
}

func skipSetup(ctx context.Context) bool {
	skip, _ := ctx.Value(skipSetupKey{}).(bool)
	return skip
}

type detachedSetupKey struct{}

// WithDetachedSetup lets a setup started under ctx outlive it: when ctx
// ends first, setup goes on and the caller gets ErrSetupContinues. Web
// requests are short; setup is not.
func WithDetachedSetup(ctx context.Context) context.Context {
	return context.WithValue(ctx, detachedSetupKey{}, true)
}

// ErrSetupContinues reports a setup still running after its caller stopped
// waiting for it.
var ErrSetupContinues = errors.New("the workspace is still being set up; its agent can start once setup is done")

// SetupWorkspace makes the item's checkout ready for work: it claims a
// checkout if there is none, then runs the setup hooks (repository files,
// the setup command) unless this claim was already set up. The item lock is
// released while setup runs; a result for a claim that changed meanwhile is
// reported, never published as the new claim's. rerun repeats a finished
// setup, refused while an agent runs in the checkout.
func (a *App) SetupWorkspace(ctx context.Context, opts ResolveOptions, rerun bool) (SetupResult, error) {
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return SetupResult{}, err
	}
	if m.State == model.StateClosed {
		_ = operation.Release()
		return SetupResult{}, fmt.Errorf("work item %s is closed", m.ID)
	}
	// An agent already working in the checkout is never set up under.
	if existing, err := a.Store.LoadAgentRuntime(m.ID); err == nil && a.primaryAgentService().ObserveOwnership(existing).ProcessAlive {
		_ = operation.Release()
		if rerun {
			return SetupResult{}, fmt.Errorf("an agent runs in %s's checkout; stop it before setting the checkout up again", m.ID)
		}
		return SetupResult{WorkItemID: m.ID, Warnings: []string{}}, nil
	}
	m, warnings, err := a.ensureWorkspaceCheckout(ctx, ResolveOptions{Selector: m.ID, CWD: opts.CWD, Env: opts.Env}, m, true)
	_ = operation.Release()
	if err != nil {
		return SetupResult{}, err
	}
	result, err := a.setupClaim(ctx, m, rerun)
	result.Warnings = append(warnings, result.Warnings...)
	return result, err
}

// ensureWorkspaceSetup keeps an agent from starting on a claim whose setup
// has not succeeded, running it now if needed.
func (a *App) ensureWorkspaceSetup(ctx context.Context, m model.Manifest) ([]string, error) {
	result, err := a.setupClaim(ctx, m, false)
	return result.Warnings, err
}

func (a *App) setupClaim(ctx context.Context, m model.Manifest, rerun bool) (SetupResult, error) {
	detached, _ := ctx.Value(detachedSetupKey{}).(bool)
	if !detached {
		return a.setupClaimNow(ctx, m, rerun)
	}
	type outcome struct {
		result SetupResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := a.setupClaimNow(context.WithoutCancel(ctx), m, rerun)
		done <- outcome{result, err}
	}()
	select {
	case finished := <-done:
		return finished.result, finished.err
	case <-ctx.Done():
		return SetupResult{WorkItemID: m.ID, Warnings: []string{}}, ErrSetupContinues
	}
}

func (a *App) setupClaimNow(ctx context.Context, m model.Manifest, rerun bool) (SetupResult, error) {
	result := SetupResult{WorkItemID: m.ID, Warnings: []string{}}
	claim := m.Checkout.Claim
	if claim == nil || claim.Path == "" {
		return result, fmt.Errorf("work item %s has no checkout to set up", m.ID)
	}
	if a.setupDir() == "" {
		return result, nil
	}
	lock, err := itemlock.Acquire(ctx, a.setupPath(m.ID, ".lock"))
	if err != nil {
		return result, err
	}
	defer lock.Release()
	previous, _ := a.readSetupRun(m.ID)
	if previous.Ready(claim.ID) && !rerun {
		result.Run = previous
		return result, nil
	}
	now := a.Clock.Now().UTC()
	run := SetupRun{ClaimID: claim.ID, Generation: claim.Generation, StartedAt: now, Log: a.setupPath(m.ID, "-"+strconv.FormatUint(claim.Generation, 10)+".log")}
	if skipSetup(ctx) {
		run.Status, run.FinishedAt, run.Log = SetupSkipped, &now, ""
		result.Run = run
		result.Warnings = append(result.Warnings, "workspace setup skipped for this checkout")
		return result, a.writeSetupRun(m.ID, run)
	}
	run.Status = SetupRunning
	if err := a.writeSetupRun(m.ID, run); err != nil {
		return result, err
	}
	log, err := os.OpenFile(run.Log, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return result, fmt.Errorf("open setup log: %w", err)
	}
	defer log.Close()
	fmt.Fprintf(log, "== setup of %s in %s at %s\n", m.ID, claim.Path, now.Format(time.RFC3339))
	warnings, setupErr := a.runSetupHooks(ctx, m, *claim, io.Writer(log))
	finished := a.Clock.Now().UTC()
	run.FinishedAt, run.Warnings, run.Status = &finished, warnings, SetupSucceeded
	if setupErr != nil {
		run.Status, run.Error = SetupFailed, setupErr.Error()
		fmt.Fprintf(log, "== failed: %s\n", setupErr)
	} else {
		fmt.Fprintf(log, "== done in %s\n", finished.Sub(now).Round(time.Millisecond))
	}
	result.Ran, result.Run = true, run
	result.Warnings = append(result.Warnings, warnings...)
	if err := a.writeSetupRun(m.ID, run); err != nil {
		return result, err
	}
	event := map[string]any{"claim_id": claim.ID, "claim_generation": claim.Generation, "status": run.Status, "duration_ms": finished.Sub(now).Milliseconds()}
	if err := a.Store.AppendEvent(ctx, m.ID, model.NewEvent(finished, "workspace.setup_finished", "user", event)); err != nil {
		result.Warnings = append(result.Warnings, "could not record workspace.setup_finished: "+err.Error())
	}
	if setupErr != nil {
		return result, fmt.Errorf("workspace setup failed: %w (log: %s); the checkout stays claimed, so retry with `wi workspace setup %s`", setupErr, run.Log, m.ID)
	}
	if current, err := a.Store.LoadManifest(m.ID); err == nil && (current.Checkout.Claim == nil || current.Checkout.Claim.ID != claim.ID) {
		return result, fmt.Errorf("the checkout of %s changed while it was being set up; the result applies to the earlier checkout only", m.ID)
	}
	if gitDir, err := worktreeGitDir(claim.Path); err == nil {
		_ = repofiles.WriteFile(filepath.Join(gitDir, setupMarker), []byte(claim.ID))
	}
	return result, nil
}

// setupMarker names, in a slot's own Git directory, the claim last set up
// there, so the next claim knows it reuses a slot.
const setupMarker = "wi-setup-claim"

func (a *App) runSetupHooks(ctx context.Context, m model.Manifest, claim model.WorkspaceClaim, log io.Writer) ([]string, error) {
	subject := plugin.Subject{ItemID: m.ID, RepositoryID: m.Repository.ID, CheckoutPath: claim.Path, BoundaryPath: claim.Path, ClaimID: claim.ID, Generation: claim.Generation, Mode: string(agent.ModeHeadless)}
	env, _, warnings, err := a.baseRuntimeEnvironment(ctx, agent.ModeHeadless, subject)
	if err != nil {
		return warnings, err
	}
	if err := a.applyRepositoryEnvironment(ctx, m.Repository.ID, env); err != nil {
		return warnings, err
	}
	reused := false
	if gitDir, err := worktreeGitDir(claim.Path); err == nil {
		if previous, err := os.ReadFile(filepath.Join(gitDir, setupMarker)); err == nil && strings.TrimSpace(string(previous)) != claim.ID {
			reused = true
		}
	}
	root := m.Repository.OperationalRoot()
	env["WI_ID"], env["WI_WORKTREE"], env["WI_REPOSITORY_ID"] = m.ID, claim.Path, m.Repository.ID
	env["WI_SLOT"], env["WI_SLOT_REUSED"] = filepath.Base(claim.Path), map[bool]string{true: "1", false: "0"}[reused]
	request := plugin.SetupRequest{
		Subject: subject, Slot: filepath.Base(claim.Path), Reused: reused,
		RepositoryRoot: root, DefaultBranch: defaultBranch(ctx, m.Repository),
		FilesDir: a.Store.RepositoryFilesDir(m.Repository.ID), Environment: env, Log: log,
	}
	hookWarnings, err := a.Preparation.SetupCheckout(ctx, request)
	for _, warning := range hookWarnings {
		fmt.Fprintf(log, "warning: %s\n", warning)
	}
	return append(warnings, hookWarnings...), err
}

// defaultBranch is the repository's integration branch, or its store's HEAD.
func defaultBranch(ctx context.Context, repository model.Repository) string {
	if branch := strings.TrimSpace(repository.IntegrationBranch); branch != "" {
		return branch
	}
	root := repository.OperationalRoot()
	if root == "" {
		return ""
	}
	output, err := exec.CommandContext(ctx, "git", "--git-dir", root, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// WorkspaceSetupStatus reports the setup of the item's current claim.
func (a *App) WorkspaceSetupStatus(ctx context.Context, opts ResolveOptions) (SetupStatus, error) {
	m, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return SetupStatus{}, err
	}
	return a.setupStatus(m), nil
}

func (a *App) setupStatus(m model.Manifest) SetupStatus {
	status := SetupStatus{WorkItemID: m.ID}
	run, err := a.readSetupRun(m.ID)
	if err != nil || run.ClaimID == "" {
		return status
	}
	status.Run = &run
	status.Current = m.Checkout.Claim != nil && m.Checkout.Claim.ID == run.ClaimID
	if run.Log != "" {
		status.LogTail = tailFile(run.Log, 4<<10)
	}
	return status
}

func (a *App) setupRuns() store.SetupRuns {
	if a.AgentRuntimeStateRoot == "" {
		return store.SetupRuns{}
	}
	return store.SetupRuns{Dir: filepath.Join(a.AgentRuntimeStateRoot, "setup")}
}

func (a *App) setupDir() string { return a.setupRuns().Dir }

func (a *App) setupPath(id, suffix string) string { return a.setupRuns().Path(id, suffix) }

func (a *App) readSetupRun(id string) (SetupRun, error) { return a.setupRuns().Read(id) }

func (a *App) writeSetupRun(id string, run SetupRun) error { return a.setupRuns().Write(id, run) }

// tailFile returns about the last limit bytes of a file, from a line start.
func tailFile(path string, limit int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	start := max(info.Size()-limit, 0)
	buffer := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(buffer, start); err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	text := string(buffer)
	if start > 0 {
		if index := strings.IndexByte(text, '\n'); index >= 0 {
			text = text[index+1:]
		}
	}
	return text
}

// worktreeGitDir is a linked worktree's own Git directory.
func worktreeGitDir(checkout string) (string, error) {
	content, err := os.ReadFile(filepath.Join(checkout, ".git"))
	if err != nil {
		return "", err
	}
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(content)), "gitdir: ")
	if !ok {
		return "", errors.New("not a linked worktree")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(checkout, dir)
	}
	return dir, nil
}

// WaitWorkspaceSetup waits for a setup of the item that is running, as one
// a request detached from, to end, and reports whether its current checkout
// is now set up. A failed setup is not retried here.
func (a *App) WaitWorkspaceSetup(ctx context.Context, opts ResolveOptions) (bool, error) {
	m, err := a.ResolveItem(ctx, opts)
	if err != nil || a.setupDir() == "" {
		return false, err
	}
	lock, err := itemlock.Acquire(ctx, a.setupPath(m.ID, ".lock"))
	if err != nil {
		return false, err
	}
	_ = lock.Release()
	if m, err = a.Store.LoadManifest(m.ID); err != nil {
		return false, err
	}
	run, err := a.readSetupRun(m.ID)
	return err == nil && m.Checkout.Claim != nil && run.Ready(m.Checkout.Claim.ID), err
}
