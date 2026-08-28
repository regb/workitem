package view

import (
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/regb/workitem/internal/model"
)

type Store interface {
	ListManifests() ([]model.Manifest, []error)
}
type Service struct{ store Store }

func New(st Store) *Service { return &Service{store: st} }

type Capacity struct {
	Active    int      `json:"active"`
	Limit     int      `json:"limit"`
	Available int      `json:"available"`
	ItemIDs   []string `json:"item_ids"`
}
type Options struct {
	IncludeArchived bool
	ArchivedOnly    bool
	State           string
	Label           string
	LabelRules      map[string]bool
}
type Item struct {
	ID             string          `json:"id"`
	Slug           string          `json:"slug"`
	Title          string          `json:"title"`
	State          string          `json:"state"`
	Labels         []string        `json:"labels"`
	DeepWork       bool            `json:"deep_work"`
	CapacityFull   bool            `json:"capacity_full,omitempty"`
	Repository     string          `json:"repository"`
	RepositoryHome bool            `json:"repository_home,omitempty"`
	Agent          *AgentStatus    `json:"agent,omitempty"`
	Worktree       *WorktreeStatus `json:"worktree,omitempty"`
	Attention      *Activity       `json:"attention,omitempty"`
	AttentionRank  int             `json:"attention_rank,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	ChangedAt      time.Time       `json:"state_changed_at"`
	Manifest       model.Manifest  `json:"manifest,omitempty"`
}
type AgentStatus struct {
	Status                 string `json:"status"`
	Label                  string `json:"label"`
	Marker                 string `json:"marker,omitempty"`
	Bucket                 string `json:"bucket"`
	Reason                 string `json:"reason,omitempty"`
	ProcessOnline          bool   `json:"process_online"`
	TurnState              string `json:"turn_state,omitempty"`
	LastActivityAgeSeconds int64  `json:"last_activity_age_seconds,omitempty"`
}
type WorktreeStatus struct {
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
	CheckoutPath   string `json:"checkout_path,omitempty"`
	Head           string `json:"head,omitempty"`
	ExpectedBranch string `json:"expected_branch,omitempty"`
	CurrentBranch  string `json:"current_branch,omitempty"`
	BranchMatches  bool   `json:"branch_matches"`
	BranchMismatch bool   `json:"branch_mismatch,omitempty"`
	Dirty          bool   `json:"dirty"`
	HasChanges     bool   `json:"has_changes"`
}
type Activity struct {
	LastRequestedAt *time.Time `json:"last_requested_at,omitempty"`
	LastCompletedAt *time.Time `json:"last_completed_at,omitempty"`
	LastDeferredAt  *time.Time `json:"last_deferred_at,omitempty"`
}
type Sections struct {
	Working  []Item `json:"working"`
	Backlog  []Item `json:"backlog"`
	Waiting  []Item `json:"waiting"`
	Archived []Item `json:"archived,omitempty"`
}
type Result struct {
	DeepWork Capacity `json:"deep_work"`
	Sections Sections `json:"sections"`
	Warnings []string `json:"warnings"`
}

func (s *Service) WorkList(opts Options, limit, folders int) Result {
	items, errs := s.store.ListManifests()
	warnings := []string{}
	for _, err := range errs {
		warnings = append(warnings, err.Error())
	}
	capacity := s.DeepWorkCapacity(limit)
	rules := opts.LabelRules
	if filter := strings.TrimSpace(opts.Label); filter != "" {
		if normalized, err := model.NormalizeLabel(filter); err == nil {
			if rules == nil {
				rules = map[string]bool{}
			}
			rules[normalized] = true
		} else {
			warnings = append(warnings, err.Error())
		}
	}
	filtered := []model.Manifest{}
	state := strings.TrimSpace(opts.State)
	for _, item := range items {
		if opts.ArchivedOnly && item.State != model.StateArchived || !opts.ArchivedOnly && !opts.IncludeArchived && item.State == model.StateArchived || state != "" && item.State != state || !matchesLabels(item.Labels, rules) {
			continue
		}
		filtered = append(filtered, item)
	}
	res := Result{DeepWork: capacity, Warnings: warnings}
	if opts.IncludeArchived || opts.ArchivedOnly {
		for _, m := range filtered {
			if m.State == model.StateArchived {
				res.Sections.Archived = append(res.Sections.Archived, makeItem(m, false, folders))
			}
		}
		sort.SliceStable(res.Sections.Archived, func(i, j int) bool { return newer(res.Sections.Archived[i], res.Sections.Archived[j]) })
		if opts.ArchivedOnly {
			return res
		}
	}
	working, waiting, deep, ordinary := []Item{}, []Item{}, []Item{}, []Item{}
	for _, m := range filtered {
		if m.State == model.StateArchived {
			continue
		}
		item := makeItem(m, false, folders)
		switch m.State {
		case model.StateWorking:
			working = append(working, item)
		case model.StateWaiting:
			waiting = append(waiting, item)
		case model.StateBacklog:
			if m.DeepWork {
				deep = append(deep, item)
			} else {
				ordinary = append(ordinary, item)
			}
		}
	}
	sort.SliceStable(working, func(i, j int) bool {
		if working[i].DeepWork != working[j].DeepWork {
			return working[i].DeepWork
		}
		if !working[i].ChangedAt.Equal(working[j].ChangedAt) {
			return working[i].ChangedAt.Before(working[j].ChangedAt)
		}
		return working[i].ID < working[j].ID
	})
	sort.SliceStable(waiting, func(i, j int) bool {
		if waiting[i].DeepWork != waiting[j].DeepWork {
			return waiting[i].DeepWork
		}
		if !waiting[i].UpdatedAt.Equal(waiting[j].UpdatedAt) {
			return waiting[i].UpdatedAt.After(waiting[j].UpdatedAt)
		}
		return waiting[i].ID < waiting[j].ID
	})
	created := func(xs []Item) {
		sort.SliceStable(xs, func(i, j int) bool {
			if !xs[i].CreatedAt.Equal(xs[j].CreatedAt) {
				return xs[i].CreatedAt.Before(xs[j].CreatedAt)
			}
			return xs[i].ID < xs[j].ID
		})
	}
	created(deep)
	created(ordinary)
	fit, excess := []Item{}, []Item{}
	for i, item := range deep {
		if i < capacity.Available {
			fit = append(fit, item)
		} else {
			item.CapacityFull = true
			excess = append(excess, item)
		}
	}
	res.Sections.Working, res.Sections.Waiting, res.Sections.Backlog = working, waiting, append(append(fit, ordinary...), excess...)
	return res
}

func (s *Service) DeepWorkCapacity(limit int) Capacity {
	items, _ := s.store.ListManifests()
	ids := []string{}
	for _, m := range items {
		if m.DeepWork && (m.State == model.StateWorking || m.State == model.StateWaiting) {
			ids = append(ids, m.ID)
		}
	}
	sort.Strings(ids)
	available := limit - len(ids)
	if available < 0 {
		available = 0
	}
	return Capacity{len(ids), limit, available, ids}
}
func matchesLabels(labels []string, rules map[string]bool) bool {
	for label, include := range rules {
		if model.HasLabel(labels, label) != include {
			return false
		}
	}
	return true
}
func ProjectItem(m model.Manifest, folders int) Item {
	return makeItem(m, false, folders)
}

func makeItem(m model.Manifest, full bool, folders int) Item {
	return Item{ID: m.ID, Slug: m.Slug, Title: m.Title, State: m.State, Labels: append([]string{}, m.Labels...), DeepWork: m.DeepWork, CapacityFull: full, Repository: repositoryName(m.Repository, folders), RepositoryHome: m.Checkout.Kind == model.WorkspaceKindRepositoryHome, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, ChangedAt: m.StateChangedAt}
}
func repositoryName(repo model.Repository, folders int) string {
	if folders < 1 {
		folders = 1
	}
	if remote := strings.TrimSpace(repo.RemoteURL); remote != "" {
		if p := remotePath(remote); p != "" {
			return lastSegments(p, folders, "/")
		}
	}
	if common := strings.TrimSpace(repo.GitCommonDir); common != "" {
		if filepath.Base(common) == ".git" {
			common = filepath.Dir(common)
		}
		return lastSegments(common, folders, string(filepath.Separator))
	}
	return lastSegments(repo.RootAtCreation, folders, string(filepath.Separator))
}
func remotePath(remote string) string {
	remote = strings.TrimSuffix(strings.TrimRight(remote, "/"), ".git")
	if u, err := url.Parse(remote); err == nil && u.Path != "" && u.Scheme != "" {
		return strings.Trim(u.Path, "/")
	}
	if i := strings.Index(remote, ":"); i >= 0 && !strings.Contains(remote[:i], "/") {
		return strings.Trim(remote[i+1:], "/")
	}
	return strings.Trim(remote, "/")
}
func lastSegments(path string, n int, sep string) string {
	path = strings.TrimSpace(path)
	if sep == string(filepath.Separator) {
		path = filepath.Clean(path)
	}
	parts := []string{}
	for _, p := range strings.Split(path, sep) {
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return path
	}
	if len(parts) > n {
		parts = parts[len(parts)-n:]
	}
	return strings.Join(parts, sep)
}
func newer(a, b Item) bool {
	if !a.UpdatedAt.Equal(b.UpdatedAt) {
		return a.UpdatedAt.After(b.UpdatedAt)
	}
	return a.ID < b.ID
}
