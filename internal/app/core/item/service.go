package item

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
)

type Store interface {
	Ensure() error
	CreateItem(context.Context, model.Manifest, ...model.Event) error
	RemoveItem(string) error
	SaveManifest(context.Context, model.Manifest) error
	AppendEvent(context.Context, string, model.Event) error
	ReadEvents(string) ([]model.Event, error)
	ListManifests() ([]model.Manifest, []error)
	ItemDir(string) string
}

type Git interface {
	DetectRepository(context.Context, string, string) (model.RepositoryInfo, error)
	DefaultBranch(context.Context, string) (string, error)
	RepositoryHome(context.Context, string) (model.RepositoryHomeInfo, error)
	BranchExists(context.Context, string, string) (bool, error)
}

type Resolver func(context.Context, contract.ResolveOptions) (model.Manifest, error)
type ProjectDefaultLabels func(repositoryRoot string) ([]string, []string, error)

type Service struct {
	store                Store
	git                  Git
	resolve              Resolver
	now                  func() time.Time
	newID                func() (string, error)
	defaultLabels        []string
	projectDefaultLabels ProjectDefaultLabels
}

func New(st Store, git Git, resolve Resolver, now func() time.Time, newID func() (string, error), defaultLabels []string, projectDefaultLabels ProjectDefaultLabels) *Service {
	return &Service{store: st, git: git, resolve: resolve, now: now, newID: newID, defaultLabels: defaultLabels, projectDefaultLabels: projectDefaultLabels}
}

type NewOptions struct {
	Title           string
	Slug            string
	Description     string
	Labels          []string
	NoDefaultLabels bool
	DeepWork        bool
	Base            string
	Home            bool
	CWD             string
	Env             map[string]string
	PrepareOnly     bool
}

type NewResult struct {
	Manifest    model.Manifest `json:"manifest"`
	ItemDir     string         `json:"item_dir"`
	Warnings    []string       `json:"warnings"`
	Description string         `json:"-"`
	CreateEvent model.Event    `json:"-"`
}

type ShowResult struct {
	Manifest    model.Manifest `json:"manifest"`
	Description string         `json:"description"`
	ItemDir     string         `json:"item_dir"`
}

type Capacity struct {
	Active    int      `json:"active"`
	Limit     int      `json:"limit"`
	Available int      `json:"available"`
	ItemIDs   []string `json:"item_ids"`
}

type StateTransitionResult struct {
	WorkItemID    string         `json:"work_item_id"`
	PreviousState string         `json:"previous_state"`
	State         string         `json:"state"`
	Changed       bool           `json:"changed"`
	Manifest      model.Manifest `json:"manifest"`
	DeepWork      bool           `json:"deep_work"`
	Capacity      *Capacity      `json:"capacity,omitempty"`
	Warnings      []string       `json:"warnings"`
}

type StateResult struct {
	WorkItemID string         `json:"work_item_id"`
	State      string         `json:"state"`
	Manifest   model.Manifest `json:"manifest"`
}

type LabelResult struct {
	WorkItemID string         `json:"work_item_id"`
	Labels     []string       `json:"labels"`
	Changed    []string       `json:"changed"`
	Manifest   model.Manifest `json:"manifest"`
	Warnings   []string       `json:"warnings"`
}

type DeepWorkResult struct {
	WorkItemID string         `json:"work_item_id"`
	DeepWork   bool           `json:"deep_work"`
	Changed    bool           `json:"changed"`
	Capacity   *Capacity      `json:"capacity,omitempty"`
	Manifest   model.Manifest `json:"manifest"`
	Warnings   []string       `json:"warnings"`
}

type EventsResult struct {
	WorkItemID string        `json:"work_item_id"`
	Events     []model.Event `json:"events"`
}

func (s *Service) NewWorkItem(ctx context.Context, opts NewOptions) (NewResult, error) {
	if s == nil || s.store == nil || s.git == nil {
		return NewResult{}, fmt.Errorf("item service is not fully configured")
	}
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		return NewResult{}, fmt.Errorf("title is required")
	}
	cwd := opts.CWD
	var err error
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return NewResult{}, fmt.Errorf("get current directory: %w", err)
		}
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return NewResult{}, fmt.Errorf("resolve current directory: %w", err)
	}
	if err = s.store.Ensure(); err != nil {
		return NewResult{}, err
	}
	repo, err := s.git.DetectRepository(ctx, cwd, opts.Base)
	if err != nil {
		return NewResult{}, err
	}
	warnings := []string{}
	defaultBranch := ""
	var home model.RepositoryHomeInfo
	if opts.Home && strings.TrimSpace(opts.Base) != "" {
		return NewResult{}, fmt.Errorf("--home and --base cannot be used together")
	}
	if strings.TrimSpace(opts.Base) == "" {
		defaultBranch, err = s.git.DefaultBranch(ctx, repo.Repository.RootAtCreation)
		if err != nil {
			if opts.Home {
				return NewResult{}, fmt.Errorf("determine local default branch for repository home: %w", err)
			}
			warnings = append(warnings, "could not determine local default branch; based new item on current HEAD: "+err.Error())
		} else {
			repo, err = s.git.DetectRepository(ctx, repo.Repository.RootAtCreation, defaultBranch)
			if err != nil {
				return NewResult{}, fmt.Errorf("resolve local default branch %q: %w", defaultBranch, err)
			}
		}
	}
	if opts.Home {
		home, err = s.git.RepositoryHome(ctx, repo.Repository.RootAtCreation)
		if err != nil {
			return NewResult{}, fmt.Errorf("inspect repository home: %w", err)
		}
		if strings.TrimSpace(home.Path) == "" || home.Bare {
			return NewResult{}, fmt.Errorf("repository has no primary working checkout; create one explicitly or omit --home")
		}
		info, statErr := os.Stat(home.Path)
		if statErr != nil {
			return NewResult{}, fmt.Errorf("repository home checkout path is unavailable: %w", statErr)
		}
		if !info.IsDir() {
			return NewResult{}, fmt.Errorf("repository home checkout path is not a directory: %s", home.Path)
		}
		if home.Detached || strings.TrimSpace(home.Branch) == "" {
			return NewResult{}, fmt.Errorf("repository home at %s is detached; expected local default branch %s", home.Path, defaultBranch)
		}
		if home.Branch != defaultBranch {
			return NewResult{}, fmt.Errorf("repository home is on branch %s; expected local default branch %s; switch it manually, then retry", home.Branch, defaultBranch)
		}
		// Keep future Git operations independent of the linked worktree from
		// which --home may have been invoked.
		repo.Repository.RootAtCreation = home.Path
	}
	labels := []string{}
	if !opts.NoDefaultLabels {
		labels = append(labels, s.defaultLabels...)
		if s.projectDefaultLabels != nil {
			projectLabels, projectWarnings, err := s.projectDefaultLabels(repo.Repository.RootAtCreation)
			warnings = append(warnings, projectWarnings...)
			if err != nil {
				return NewResult{}, err
			}
			labels = append(labels, projectLabels...)
		}
		environmentLabels, err := itemDefaultLabelsFromEnv(opts.Env)
		if err != nil {
			return NewResult{}, err
		}
		labels = append(labels, environmentLabels...)
	}
	labels = append(labels, opts.Labels...)
	labels, err = model.NormalizeLabels(labels)
	if err != nil {
		return NewResult{}, fmt.Errorf("labels: %w", err)
	}
	id, err := s.newID()
	if err != nil {
		return NewResult{}, err
	}
	slug, err := s.chooseSlug(opts.Slug, title)
	if err != nil {
		return NewResult{}, err
	}
	checkout := model.Checkout{Kind: model.WorkspaceKindManagedSlot, Branch: model.ItemBranchName(slug, id)}
	if opts.Home {
		path := home.Path
		checkout = model.Checkout{Kind: model.WorkspaceKindRepositoryHome, Path: &path, Branch: defaultBranch}
	} else {
		exists, err := s.git.BranchExists(ctx, repo.Repository.RootAtCreation, checkout.Branch)
		if err != nil {
			return NewResult{}, err
		}
		if exists {
			return NewResult{}, fmt.Errorf("implementation branch %q already exists", checkout.Branch)
		}
	}
	now := s.now()
	m := model.NewManifest(id, slug, title, labels, opts.DeepWork, now, repo.Repository, checkout)
	createEvent := model.NewEvent(now, "work_item.created", "user", map[string]any{"title": title, "slug": slug, "workspace_kind": checkout.Kind})
	if opts.PrepareOnly {
		return NewResult{Manifest: m, ItemDir: s.store.ItemDir(id), Warnings: warnings, Description: opts.Description, CreateEvent: createEvent}, nil
	}
	if err = s.store.CreateItem(ctx, m, createEvent); err != nil {
		return NewResult{}, err
	}
	path := filepath.Join(s.store.ItemDir(id), model.DescriptionFilename)
	if err = os.WriteFile(path, []byte(opts.Description), 0600); err != nil {
		_ = s.store.RemoveItem(id)
		return NewResult{}, fmt.Errorf("write description file: %w", err)
	}
	return NewResult{Manifest: m, ItemDir: s.store.ItemDir(id), Warnings: warnings}, nil
}

func itemDefaultLabelsFromEnv(env map[string]string) ([]string, error) {
	raw := strings.TrimSpace(env["WI_ITEM_DEFAULT_LABELS"])
	if raw == "" {
		return nil, nil
	}
	labels := []string{}
	for _, label := range strings.Split(raw, ",") {
		if label = strings.TrimSpace(label); label != "" {
			labels = append(labels, label)
		}
	}
	normalized, err := model.NormalizeLabels(labels)
	if err != nil {
		return nil, fmt.Errorf("WI_ITEM_DEFAULT_LABELS: %w", err)
	}
	return normalized, nil
}

func (s *Service) Show(ctx context.Context, opts contract.ResolveOptions) (ShowResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return ShowResult{}, err
	}
	b, err := os.ReadFile(filepath.Join(s.store.ItemDir(m.ID), model.DescriptionFilename))
	if err != nil && !os.IsNotExist(err) {
		return ShowResult{}, fmt.Errorf("read description file: %w", err)
	}
	return ShowResult{Manifest: m, Description: string(b), ItemDir: s.store.ItemDir(m.ID)}, nil
}

func (s *Service) UniqueSlug(base string) (string, error) {
	return s.chooseSlug("", base)
}

func (s *Service) chooseSlug(explicit, title string) (string, error) {
	base := model.Slugify(title)
	if strings.TrimSpace(explicit) != "" {
		base = model.Slugify(explicit)
	}
	items, errs := s.store.ListManifests()
	if len(errs) > 0 {
		return "", fmt.Errorf("inspect existing slugs: %w", errs[0])
	}
	used := map[string]bool{}
	for _, m := range items {
		if m.State != model.StateArchived && m.Slug != "" {
			used[m.Slug] = true
		}
	}
	if strings.TrimSpace(explicit) != "" {
		if used[base] {
			return "", fmt.Errorf("slug %q is already taken by an active work item", base)
		}
		return base, nil
	}
	if base == "" {
		base = "item"
	}
	if !used[base] {
		return base, nil
	}
	for i := 2; i <= 10000; i++ {
		suffix := fmt.Sprintf("-%d", i)
		limit := model.MaxSlugLength - len(suffix)
		x := strings.Trim(base[:min(len(base), limit)], "-") + suffix
		if !used[x] {
			return x, nil
		}
	}
	return "", fmt.Errorf("could not allocate a unique slug for %q", base)
}
