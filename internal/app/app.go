package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/regb/workitem/internal/agent"
	tmuxporcelain "github.com/regb/workitem/internal/app/adapter/tmux/porcelain"
	terminaladapter "github.com/regb/workitem/internal/app/adapter/tmux/terminal"
	"github.com/regb/workitem/internal/app/contract"
	attentioncore "github.com/regb/workitem/internal/app/core/attention"
	itemcore "github.com/regb/workitem/internal/app/core/item"
	agentcore "github.com/regb/workitem/internal/app/core/primaryagent"
	workspacecore "github.com/regb/workitem/internal/app/core/workspace"
	viewapp "github.com/regb/workitem/internal/app/view"
	"github.com/regb/workitem/internal/config"
	direnvpkg "github.com/regb/workitem/internal/direnv"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	piadapter "github.com/regb/workitem/internal/pi"
	processpkg "github.com/regb/workitem/internal/process"
	"github.com/regb/workitem/internal/store"
	tmuxpkg "github.com/regb/workitem/internal/tmux"
)

type Git interface {
	DetectRepository(ctx context.Context, dir, revision string) (gitpkg.RepositoryInfo, error)
	DefaultBranch(ctx context.Context, repoRoot string) (string, error)
	RepositoryHome(ctx context.Context, repoRoot string) (model.RepositoryHomeInfo, error)
	Head(ctx context.Context, dir string) (string, error)
	StatusPorcelain(ctx context.Context, dir string) (string, error)
	CurrentBranch(ctx context.Context, dir string) (string, error)
	BranchExists(ctx context.Context, repoRoot, branch string) (bool, error)
	WorktreeAdd(ctx context.Context, opts gitpkg.WorktreeAddOptions) error
	Switch(ctx context.Context, dir, branch, startPoint string, create bool) error
	WorktreeRemove(ctx context.Context, repoRoot, path string, force bool) error
}

type Tmux interface {
	HasSession(ctx context.Context, name string) (bool, error)
	EnsureSession(ctx context.Context, spec tmuxpkg.SessionSpec) (bool, error)
	LaunchCommand(ctx context.Context, spec tmuxpkg.LaunchSpec) error
	PaneInfo(ctx context.Context, target string) (tmuxpkg.PaneInfo, error)
	AttachOrSwitch(ctx context.Context, name string, inTmux bool) error
	KillSession(ctx context.Context, name string) error
	KillSessionAsync(ctx context.Context, name string) error
	CurrentSession(ctx context.Context) (string, error)
	SessionEnvironment(ctx context.Context, name string) (map[string]string, error)
	GlobalEnvironment(ctx context.Context) (map[string]string, error)
}

type ProcessInspector interface {
	Alive(pid int) bool
	Info(pid int) (model.ProcessInfo, error)
	TerminateGroup(rootPID int) error
	FindDescendant(rootPID int, needles []string) (processpkg.Info, bool, error)
}

type Pi interface {
	ExecMode(ctx context.Context, spec piadapter.ExecSpec) error
	SessionCWD(path string) (string, error)
	ForkSession(ctx context.Context, sourcePath, targetPath, targetCWD string) error
}

type Direnv interface {
	Status(ctx context.Context, dir string) (direnvpkg.Status, error)
	Allow(ctx context.Context, rcPath string) error
	Deny(ctx context.Context, rcPath string) error
	Environment(ctx context.Context, dir string, base map[string]string) (map[string]string, error)
}

type Store interface {
	Ensure() error
	CreateItem(ctx context.Context, manifest model.Manifest, events ...model.Event) error
	SaveManifest(ctx context.Context, manifest model.Manifest) error
	LoadTerminalRuntime(id string) (*model.TerminalRuntime, error)
	SaveTerminalRuntime(ctx context.Context, id string, workspace model.TerminalRuntime) error
	RemoveTerminalRuntime(ctx context.Context, id string) error
	LoadAgentRuntime(id string) (*model.AgentRuntime, error)
	SaveAgentRuntime(ctx context.Context, id string, runtime model.AgentRuntime) error
	AppendEvent(ctx context.Context, id string, event model.Event) error
	ReadEvents(id string) ([]model.Event, error)
	RemoveItem(id string) error
	LockPath(id string) string
	LoadManifest(id string) (model.Manifest, error)
	ClaimRepositoryHome(ctx context.Context, manifest model.Manifest) error
	ListManifests() ([]model.Manifest, []error)
	Resolve(selector string) (model.Manifest, error)
	ResolveActiveSlug(slug string) (model.Manifest, error)
	FindByWorktree(path string) (model.Manifest, error)
	ItemDir(id string) string
	WorktreesDir() string
}

type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type App struct {
	Store                      Store
	Git                        Git
	Tmux                       Tmux
	Pi                         Pi
	Direnv                     Direnv
	Process                    ProcessInspector
	Clock                      Clock
	NewID                      func() (string, error)
	SelfPath                   string
	DaemonSocketPath           string
	AgentRuntimeStateRoot      string
	AgentRuntimeSocketRoot     string
	AgentRuntimeReadyTimeout   time.Duration
	AgentRuntimeReadyInterval  time.Duration
	ArchiveRuntimeStopTimeout  time.Duration
	ShutdownRuntimeStopTimeout time.Duration
	DeepWorkConfig             config.DeepWorkConfig
	ItemConfig                 config.ItemConfig
	ListConfig                 config.ListConfig
	AgentStatusConfig          config.AgentStatusConfig
	AttentionConfig            config.AttentionConfig
	DirenvConfig               config.DirenvConfig
	ApproveDirenv              agentcore.DirenvApprover
	AgentRuntimeLauncher       agent.Launcher
	PiSessionObserver          agentcore.PiSessionObserver
	WorkListObserver           viewapp.Observe
}

func New(st Store, git Git) *App {
	cfg := config.Default()
	a := &App{Store: st, Git: git, Process: processpkg.New(), Clock: realClock{}, NewID: model.NewID, DeepWorkConfig: cfg.DeepWork, ItemConfig: cfg.Item, ListConfig: cfg.List, AgentStatusConfig: cfg.AgentStatus, AttentionConfig: cfg.Attention, DirenvConfig: cfg.Direnv, AgentRuntimeLauncher: agent.ExecLauncher{}}
	return a
}

type ResolveOptions = contract.ResolveOptions

func (a *App) ResolveItem(ctx context.Context, opts ResolveOptions) (model.Manifest, error) {
	selector := strings.TrimSpace(opts.Selector)
	if selector != "" {
		return a.Store.Resolve(selector)
	}
	if opts.Env != nil {
		if id := strings.TrimSpace(opts.Env["WI_ID"]); id != "" {
			return a.Store.Resolve(id)
		}
	}
	if inTmux(opts.Env) && a.Tmux != nil {
		if session, err := a.Tmux.CurrentSession(ctx); err == nil && strings.TrimSpace(session) != "" {
			if env, err := a.Tmux.SessionEnvironment(ctx, session); err == nil {
				if id := strings.TrimSpace(env["WI_ID"]); id != "" {
					return a.Store.Resolve(id)
				}
			}
		}
	}
	if opts.CWD != "" {
		if m, err := a.Store.FindByWorktree(opts.CWD); err == nil {
			return m, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return model.Manifest{}, err
		}
	}
	return model.Manifest{}, fmt.Errorf("no work item selected; pass an item ID, active slug, unique keyword, or set WI_ID")
}

// Area services are assembled on demand from the composition root's current ports.
func (a *App) itemService() *itemcore.Service {
	return itemcore.New(a.Store, a.Git, a.ResolveItem, a.now, func() (string, error) { return a.NewID() }, a.ItemConfig.Defaults.Labels, func(repositoryRoot string) ([]string, []string, error) {
		project, warnings, err := config.LoadProjectForRepository(repositoryRoot)
		return project.Item.Defaults.Labels, warnings, err
	})
}
func (a *App) workspaceService() *workspacecore.Service {
	return workspacecore.New(a.Store, a.Git, a.Tmux, a.Direnv, a.ResolveItem, func(runtime *model.AgentRuntime) bool {
		return a.primaryAgentService().ObserveOwnership(runtime).ProcessAlive
	}, a.now)
}
func (a *App) primaryAgentService() *agentcore.Service {
	service := agentcore.New(a.Store, a.Process, a.Git, a.Tmux, a.ResolveItem, func() (string, error) { return a.NewID() }, a.now, func(ctx context.Context, itemID string) (bool, error) {
		status, err := a.AgentStatus(ctx, AgentStatusOptions{ResolveOptions: ResolveOptions{Selector: itemID}})
		return status.Status == "busy", err
	})
	service.PiSessionObserver = a.PiSessionObserver
	service.RuntimeStateRoot = a.AgentRuntimeStateRoot
	service.RuntimeSocketRoot = a.AgentRuntimeSocketRoot
	return service
}
func (a *App) attentionService() *attentioncore.Service {
	return attentioncore.New(a.Store, a.ResolveItem, a.now)
}
func (a *App) viewService() *viewapp.Service { return viewapp.New(a.Store) }
func (a *App) terminalService() *terminaladapter.Service {
	return terminaladapter.New(a.Store, a.Tmux, a.ResolveItem, a.ensureWorkspaceCheckout, func(runtime *model.AgentRuntime) (bool, string) {
		ownership := a.primaryAgentService().ObserveOwnership(runtime)
		return ownership.ProcessAlive, ownership.Mode
	}, a.now)
}
func (a *App) tmuxPorcelainService() *tmuxporcelain.Service {
	return tmuxporcelain.New(a.ResolveItem)
}

func (a *App) now() time.Time {
	if a.Clock == nil {
		return time.Now()
	}
	return a.Clock.Now()
}

// CompositionResult reports resources assembled by adapter porcelain or a
// cross-cutting use case. A core workspace primitive may return this shared
// envelope with only Checkout populated; it never populates adapter/runtime
// fields itself.
type CompositionResult struct {
	WorkItemID   string              `json:"work_item_id"`
	Checkout     model.Checkout      `json:"checkout"`
	Terminal     *TerminalResult     `json:"terminal,omitempty"`
	PiSession    *model.PiSession    `json:"pi_session,omitempty"`
	PiLaunched   bool                `json:"pi_launched,omitempty"`
	AgentRuntime *model.AgentRuntime `json:"agent_runtime,omitempty"`
	Manifest     model.Manifest      `json:"manifest"`
	Warnings     []string            `json:"warnings"`
}
