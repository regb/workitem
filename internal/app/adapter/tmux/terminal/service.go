package terminal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
	tmuxpkg "github.com/regb/workitem/internal/tmux"
)

type Store interface {
	ItemDir(string) string
	LoadAgentRuntime(string) (*model.AgentRuntime, error)
	RemoveTerminalRuntime(context.Context, string) error
	AppendEvent(context.Context, string, model.Event) error
}

type Tmux interface {
	HasSession(context.Context, string) (bool, error)
	CurrentSession(context.Context) (string, error)
	EnsureSession(context.Context, tmuxpkg.SessionSpec) (bool, error)
	AttachOrSwitch(context.Context, string, bool) error
	KillSession(context.Context, string) error
}

type Resolver func(context.Context, contract.ResolveOptions) (model.Manifest, error)
type WorkspaceEnsurer func(context.Context, contract.ResolveOptions, model.Manifest, bool) (model.Manifest, []string, error)
type OwnershipObserver func(*model.AgentRuntime) (alive bool, mode string)

type Service struct {
	store           Store
	tmux            Tmux
	resolve         Resolver
	ensureWorkspace WorkspaceEnsurer
	ownership       OwnershipObserver
	now             func() time.Time
}

func New(st Store, t Tmux, r Resolver, ensure WorkspaceEnsurer, ownership OwnershipObserver, now func() time.Time) *Service {
	return &Service{store: st, tmux: t, resolve: r, ensureWorkspace: ensure, ownership: ownership, now: now}
}

type StatusResult struct {
	WorkItemID string   `json:"work_item_id"`
	Session    string   `json:"session"`
	Exists     bool     `json:"exists"`
	Inspected  bool     `json:"inspected"`
	Current    bool     `json:"current"`
	Warnings   []string `json:"warnings"`
}

type Result struct {
	WorkItemID string         `json:"work_item_id"`
	Session    string         `json:"session"`
	Created    bool           `json:"created"`
	Attached   bool           `json:"attached"`
	Checkout   model.Checkout `json:"checkout"`
	Warnings   []string       `json:"warnings"`
}

type CloseResult struct {
	WorkItemID string   `json:"work_item_id"`
	Session    string   `json:"session"`
	Changed    bool     `json:"changed"`
	Warnings   []string `json:"warnings"`
}

func InTmux(env map[string]string) bool { return env != nil && strings.TrimSpace(env["TMUX"]) != "" }

func (s *Service) Status(ctx context.Context, opts contract.ResolveOptions) (StatusResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return StatusResult{}, err
	}
	res := StatusResult{WorkItemID: m.ID, Session: m.TerminalSessionName(), Warnings: []string{}}
	if s.tmux == nil || strings.TrimSpace(res.Session) == "" {
		return res, nil
	}
	res.Inspected = true
	res.Exists, err = s.tmux.HasSession(ctx, res.Session)
	if err != nil {
		res.Warnings = append(res.Warnings, "could not inspect tmux terminal: "+err.Error())
	}
	if InTmux(opts.Env) {
		if current, err := s.tmux.CurrentSession(ctx); err == nil {
			res.Current = current == res.Session
		}
	}
	return res, nil
}

func (s *Service) Ensure(ctx context.Context, opts contract.ResolveOptions) (Result, error) {
	m, err := s.resolveTerminalItem(ctx, opts)
	if err != nil {
		return Result{}, err
	}
	return s.EnsureForManifest(ctx, opts, m, false)
}

func (s *Service) Enter(ctx context.Context, opts contract.ResolveOptions, attach bool) (Result, error) {
	res, err := s.Ensure(ctx, opts)
	if err != nil {
		return res, err
	}
	return s.attach(ctx, opts, res, attach)
}

// EnterExisting attaches to an already-running terminal without ensuring or
// validating its checkout. This is the repair path for a verified live TUI
// whose checkout has drifted and must remain reachable to be fixed.
func (s *Service) EnterExisting(ctx context.Context, opts contract.ResolveOptions, attach bool) (Result, error) {
	m, err := s.resolveTerminalItem(ctx, opts)
	if err != nil {
		return Result{}, err
	}
	res := Result{WorkItemID: m.ID, Session: m.TerminalSessionName(), Checkout: m.Checkout, Warnings: []string{}}
	if s.tmux == nil {
		return Result{}, fmt.Errorf("tmux terminal adapter is not configured")
	}
	exists, err := s.tmux.HasSession(ctx, res.Session)
	if err != nil {
		return Result{}, fmt.Errorf("could not inspect existing tmux terminal: %w", err)
	}
	if !exists {
		return Result{}, fmt.Errorf("active TUI runtime terminal %s does not exist", res.Session)
	}
	return s.attach(ctx, opts, res, attach)
}

func (s *Service) attach(ctx context.Context, opts contract.ResolveOptions, res Result, attach bool) (Result, error) {
	if !attach {
		return res, nil
	}
	inTmux := InTmux(opts.Env)
	client := strings.TrimSpace(opts.Env["WI_TMUX_CLIENT"])
	// An untargeted display-message can resolve to a session that was just
	// created by this process rather than the popup's originating client. When
	// the binding captured a client explicitly, always issue switch-client for
	// that client; it is idempotent and avoids falsely treating creation as an
	// already-completed switch.
	if inTmux && client == "" {
		if current, err := s.tmux.CurrentSession(ctx); err == nil && current == res.Session {
			res.Attached = true
			res.Warnings = append(res.Warnings, "already in tmux terminal "+res.Session)
			return res, nil
		}
	}
	var err error
	if switcher, ok := s.tmux.(interface {
		AttachOrSwitchClient(context.Context, string, bool, string) error
	}); ok && client != "" {
		err = switcher.AttachOrSwitchClient(ctx, res.Session, inTmux, client)
	} else {
		err = s.tmux.AttachOrSwitch(ctx, res.Session, inTmux)
	}
	if err != nil {
		return Result{}, err
	}
	res.Attached = true
	return res, nil
}

func (s *Service) Close(ctx context.Context, opts contract.ResolveOptions) (CloseResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return CloseResult{}, err
	}
	res := CloseResult{WorkItemID: m.ID, Session: m.TerminalSessionName(), Warnings: []string{}}
	if s.tmux == nil || strings.TrimSpace(res.Session) == "" {
		return res, nil
	}
	if runtime, err := s.store.LoadAgentRuntime(m.ID); err != nil {
		return res, err
	} else if s.ownership != nil {
		alive, mode := s.ownership(runtime)
		if alive && mode == "tui" {
			return res, fmt.Errorf("TUI agent runtime %s is still active; run `wi agent runtime stop --item %s`, wait for it to exit, then close the terminal", runtime.ID, m.ID)
		}
	}
	exists, err := s.tmux.HasSession(ctx, res.Session)
	if err != nil {
		return res, fmt.Errorf("could not inspect tmux terminal: %w", err)
	}
	if !exists {
		if err := s.store.RemoveTerminalRuntime(ctx, m.ID); err != nil {
			res.Warnings = append(res.Warnings, "could not remove stale terminal handle cache: "+err.Error())
		}
		return res, nil
	}
	if InTmux(opts.Env) {
		current, err := s.tmux.CurrentSession(ctx)
		if err != nil {
			return res, fmt.Errorf("could not determine current tmux terminal: %w", err)
		}
		if current == res.Session {
			return res, fmt.Errorf("cannot close current tmux terminal %s; run this command from outside it", res.Session)
		}
	}
	if err := s.tmux.KillSession(ctx, res.Session); err != nil {
		return res, fmt.Errorf("could not close tmux terminal: %w", err)
	}
	res.Changed = true
	if err := s.store.RemoveTerminalRuntime(ctx, m.ID); err != nil {
		res.Warnings = append(res.Warnings, "could not remove terminal handle cache: "+err.Error())
	}
	_ = s.store.AppendEvent(ctx, m.ID, model.NewEvent(s.now(), "terminal.closed", "user", map[string]any{"session": res.Session}))
	return res, nil
}

func (s *Service) EnsureForManifest(ctx context.Context, opts contract.ResolveOptions, m model.Manifest, createCheckout bool) (Result, error) {
	return s.EnsureForManifestWithEnvironment(ctx, opts, m, createCheckout, nil)
}

func (s *Service) EnsureForManifestWithEnvironment(ctx context.Context, opts contract.ResolveOptions, m model.Manifest, createCheckout bool, environment map[string]string) (Result, error) {
	return s.EnsureForManifestWithEnvironmentAndScrub(ctx, opts, m, createCheckout, environment, nil)
}

func (s *Service) EnsureForManifestWithEnvironmentAndScrub(ctx context.Context, opts contract.ResolveOptions, m model.Manifest, createCheckout bool, environment map[string]string, scrub []string) (Result, error) {
	m, warnings, err := s.ensureWorkspace(ctx, opts, m, createCheckout)
	if err != nil {
		return Result{}, err
	}
	res := Result{WorkItemID: m.ID, Session: m.TerminalSessionName(), Checkout: m.Checkout, Warnings: warnings}
	if s.tmux == nil {
		return res, fmt.Errorf("tmux terminal adapter is not configured")
	}
	created, err := s.tmux.EnsureSession(ctx, s.SpecWithEnvironmentAndScrub(m, environment, scrub))
	if err != nil {
		return Result{}, err
	}
	res.Created = created
	if created {
		_ = s.store.AppendEvent(ctx, m.ID, model.NewEvent(s.now(), "terminal.created", "wi", map[string]any{"session": res.Session}))
	}
	return res, nil
}

func (s *Service) resolveTerminalItem(ctx context.Context, opts contract.ResolveOptions) (model.Manifest, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return model.Manifest{}, err
	}
	if m.State == model.StateArchived {
		return model.Manifest{}, fmt.Errorf("work item %s is archived; set it to backlog before ensuring terminal access", m.ID)
	}
	return m, nil
}

func (s *Service) Spec(m model.Manifest) tmuxpkg.SessionSpec {
	return s.SpecWithEnvironment(m, nil)
}

func (s *Service) SpecWithEnvironment(m model.Manifest, environment map[string]string) tmuxpkg.SessionSpec {
	return s.SpecWithEnvironmentAndScrub(m, environment, nil)
}

func (s *Service) SpecWithEnvironmentAndScrub(m model.Manifest, environment map[string]string, scrub []string) tmuxpkg.SessionSpec {
	spec := s.spec(m, environment)
	spec.Scrub = scrub
	return spec
}

func (s *Service) spec(m model.Manifest, environment map[string]string) tmuxpkg.SessionSpec {
	env := map[string]string{}
	for _, key := range []string{"HOME", "PATH", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME"} {
		if value := os.Getenv(key); value != "" {
			env[key] = value
		}
	}
	for key, value := range environment {
		env[key] = value
	}
	// Identity and conversation-location variables are always authoritative;
	// an approved .envrc cannot redirect the session to another item.
	env["WI_ID"] = m.ID
	env["WI_DIR"] = s.store.ItemDir(m.ID)
	env["PI_CODING_AGENT_SESSION_DIR"] = filepath.Join(s.store.ItemDir(m.ID), "sessions", "pi")
	if m.Repository.RootAtCreation != "" {
		env["WI_REPOSITORY"] = m.Repository.RootAtCreation
	}
	cwd := m.Repository.RootAtCreation
	if m.Checkout.Path != nil && *m.Checkout.Path != "" {
		env["WI_WORKTREE"], cwd = *m.Checkout.Path, *m.Checkout.Path
	}
	return tmuxpkg.SessionSpec{Name: m.TerminalSessionName(), CWD: cwd, Env: env}
}
