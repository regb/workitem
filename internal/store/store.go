package store

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	itemlock "github.com/regb/workitem/internal/lock"
	"github.com/regb/workitem/internal/model"
)

const (
	manifestFile        = "manifest.json"
	eventsFile          = "events.jsonl"
	agentRuntimeFile    = "agent-runtime.json"
	terminalRuntimeFile = "terminal-runtime.json"
)

var ErrNotFound = errors.New("work item not found")

// AmbiguousError is returned when a selector matches more than one work item.
type AmbiguousError struct {
	Selector   string
	Candidates []model.Manifest
}

func (e AmbiguousError) Error() string {
	ids := make([]string, 0, len(e.Candidates))
	for _, c := range e.Candidates {
		ids = append(ids, c.ID+" ("+c.Slug+")")
	}
	return fmt.Sprintf("work item selector %q is ambiguous: %s", e.Selector, strings.Join(ids, ", "))
}

type Store struct {
	Root string
}

func New(root string) *Store { return &Store{Root: root} }

func (s *Store) ItemsDir() string { return filepath.Join(s.Root, "items") }

func (s *Store) WorktreesDir() string { return filepath.Join(s.Root, "worktrees") }

func (s *Store) LocksDir() string { return filepath.Join(s.Root, "locks") }

func (s *Store) CreationLockPath() string { return filepath.Join(s.LocksDir(), "create.lock") }

func (s *Store) WorktreeDir(id string) string { return filepath.Join(s.WorktreesDir(), id) }

func (s *Store) ItemDir(id string) string { return filepath.Join(s.ItemsDir(), id) }

func (s *Store) ManifestPath(id string) string { return filepath.Join(s.ItemDir(id), manifestFile) }

func (s *Store) EventsPath(id string) string { return filepath.Join(s.ItemDir(id), eventsFile) }

func (s *Store) TerminalRuntimePath(id string) string {
	return filepath.Join(s.ItemDir(id), terminalRuntimeFile)
}

func (s *Store) AgentRuntimePath(id string) string {
	return filepath.Join(s.ItemDir(id), agentRuntimeFile)
}

// LockPath is outside the item directory so deletion cannot replace the lock
// inode while another mutation is waiting on it.
func (s *Store) LockPath(id string) string {
	return filepath.Join(s.LocksDir(), "items", id+".lock")
}

func (s *Store) Ensure() error {
	for _, dir := range []string{s.Root, s.ItemsDir(), s.WorktreesDir(), s.LocksDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

// CreateItem creates a new work-item directory and rolls it back on failure.
func (s *Store) CreateItem(ctx context.Context, manifest model.Manifest, events ...model.Event) (err error) {
	if !model.ValidID(manifest.ID) {
		return fmt.Errorf("invalid work item id %q", manifest.ID)
	}
	if err := s.Ensure(); err != nil {
		return err
	}
	createLock, err := itemlock.Acquire(ctx, s.CreationLockPath())
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := createLock.Release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	if err := s.validateRepositoryHomeClaim(manifest); err != nil {
		return err
	}
	itemDir := s.ItemDir(manifest.ID)
	if err := os.Mkdir(itemDir, 0o700); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("work item id collision %s", manifest.ID)
		}
		return fmt.Errorf("create item directory: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = os.RemoveAll(itemDir)
		}
	}()

	for _, rel := range []string{
		filepath.Join("sessions", "pi"),
		"locks",
	} {
		if err := os.MkdirAll(filepath.Join(itemDir, rel), 0o700); err != nil {
			return fmt.Errorf("create item subdirectory %s: %w", rel, err)
		}
	}
	if err := normalizeManifest(&manifest); err != nil {
		return err
	}
	descriptionPath := filepath.Join(itemDir, model.DescriptionFilename)
	if err := os.WriteFile(descriptionPath, []byte(""), 0o600); err != nil {
		return fmt.Errorf("create description file: %w", err)
	}

	l, err := itemlock.Acquire(ctx, s.LockPath(manifest.ID))
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := l.Release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()

	if err := writeManifestAtomic(s.ManifestPath(manifest.ID), manifest); err != nil {
		return err
	}
	for _, ev := range events {
		if err := appendEventNoLock(s.EventsPath(manifest.ID), ev); err != nil {
			return err
		}
	}
	if err := fsyncDir(itemDir); err != nil {
		return err
	}
	rollback = false
	return nil
}

func (s *Store) SaveDescription(ctx context.Context, id, description string) (err error) {
	if !model.ValidID(id) {
		return fmt.Errorf("invalid work item id %q", id)
	}
	l, err := itemlock.Acquire(ctx, s.LockPath(id))
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := l.Release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	if err := s.requireNativeItem(id); err != nil {
		return err
	}
	return writeBytesAtomic(filepath.Join(s.ItemDir(id), model.DescriptionFilename), ".description-*.tmp", []byte(description), "description")
}

func (s *Store) RemoveItem(id string) error {
	if !model.ValidID(id) {
		return fmt.Errorf("invalid work item id %q", id)
	}
	return os.RemoveAll(s.ItemDir(id))
}

func (s *Store) requireItem(id string) error {
	if _, err := os.Stat(s.ManifestPath(id)); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("inspect work item %s: %w", id, err)
	}
	return nil
}

func (s *Store) requireNativeItem(id string) error {
	if info, err := os.Stat(s.ItemDir(id)); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("inspect native work item %s: %w", id, err)
	} else if !info.IsDir() {
		return fmt.Errorf("native work item %s is not a directory", id)
	}
	return nil
}

func (s *Store) LoadManifest(id string) (model.Manifest, error) {
	var m model.Manifest
	if !model.ValidID(id) {
		return m, fmt.Errorf("invalid work item id %q", id)
	}
	b, err := os.ReadFile(s.ManifestPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return m, ErrNotFound
		}
		return m, fmt.Errorf("read manifest: %w", err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("parse manifest %s: %w", s.ManifestPath(id), err)
	}
	if err := normalizeManifest(&m); err != nil {
		return m, fmt.Errorf("invalid manifest %s: %w", s.ManifestPath(id), err)
	}
	return m, nil
}

func (s *Store) validateRepositoryHomeClaim(manifest model.Manifest) error {
	if manifest.Checkout.Kind != model.WorkspaceKindRepositoryHome || !manifest.Checkout.Present() {
		return nil
	}
	items, itemErrs := s.ListManifests()
	if len(itemErrs) > 0 {
		return fmt.Errorf("inspect repository-home claims: %w", itemErrs[0])
	}
	for _, item := range items {
		if item.ID != manifest.ID && item.Checkout.Kind == model.WorkspaceKindRepositoryHome && item.Checkout.Present() && sameRepositoryIdentity(item.Repository, manifest.Repository) {
			return fmt.Errorf("repository home is already claimed by work item %s (%s)", item.ID, item.Slug)
		}
	}
	return nil
}

func sameRepositoryIdentity(left, right model.Repository) bool {
	leftKey := strings.TrimSpace(left.GitCommonDir)
	rightKey := strings.TrimSpace(right.GitCommonDir)
	if leftKey == "" || rightKey == "" {
		leftKey = strings.TrimSpace(left.RootAtCreation)
		rightKey = strings.TrimSpace(right.RootAtCreation)
	}
	leftAbs, leftErr := filepath.Abs(leftKey)
	rightAbs, rightErr := filepath.Abs(rightKey)
	return leftErr == nil && rightErr == nil && leftAbs == rightAbs
}

// NormalizeManifest validates and canonicalizes a manifest without writing it.
func NormalizeManifest(manifest model.Manifest) (model.Manifest, error) {
	if err := normalizeManifest(&manifest); err != nil {
		return model.Manifest{}, err
	}
	return manifest, nil
}

func normalizeManifest(m *model.Manifest) error {
	if m == nil {
		return fmt.Errorf("manifest is nil")
	}
	if !model.ValidID(m.ID) {
		return fmt.Errorf("invalid work item id %q", m.ID)
	}
	m.Slug = strings.TrimSpace(m.Slug)
	if strings.TrimSpace(m.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if m.State != model.StateBacklog && m.State != model.StateWorking && m.State != model.StateWaiting && m.State != model.StateArchived {
		return fmt.Errorf("invalid state %q", m.State)
	}
	if m.State != model.StateArchived && m.Slug == "" {
		return fmt.Errorf("slug is required for non-archived work items")
	}
	if m.State == model.StateArchived && m.Slug != "" {
		return fmt.Errorf("slug must be empty for archived work items")
	}
	if m.StateChangedAt.IsZero() {
		return fmt.Errorf("state_changed_at is required")
	}
	if m.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	if m.UpdatedAt.IsZero() {
		return fmt.Errorf("updated_at is required")
	}
	remote, err := model.SanitizeRemoteURL(m.Repository.RemoteURL)
	if err != nil {
		return fmt.Errorf("repository remote_url: %w", err)
	}
	m.Repository.RemoteURL = remote
	labels, err := model.NormalizeLabels(m.Labels)
	if err != nil {
		return fmt.Errorf("labels: %w", err)
	}
	m.Labels = labels
	if m.Labels == nil {
		m.Labels = []string{}
	}
	if m.Checkout.Kind == "" {
		m.Checkout.Kind = model.WorkspaceKindManagedSlot
	}
	if m.Checkout.Kind != model.WorkspaceKindManagedSlot && m.Checkout.Kind != model.WorkspaceKindRepositoryHome {
		return fmt.Errorf("unsupported checkout kind %q", m.Checkout.Kind)
	}
	if strings.TrimSpace(m.Checkout.Branch) == "" {
		return fmt.Errorf("checkout branch is required")
	}
	if m.Checkout.Path != nil && strings.TrimSpace(*m.Checkout.Path) == "" {
		return fmt.Errorf("checkout path must not be empty")
	}
	if m.Checkout.Kind == model.WorkspaceKindRepositoryHome && strings.TrimSpace(m.Repository.RootAtCreation) == "" {
		return fmt.Errorf("repository-home checkout requires repository root")
	}
	return nil
}

// ClaimRepositoryHome atomically validates repository-home exclusivity and
// persists the claim under the same global lock used by item creation.
func (s *Store) ClaimRepositoryHome(ctx context.Context, manifest model.Manifest) (err error) {
	if err := s.Ensure(); err != nil {
		return err
	}
	claimLock, err := itemlock.Acquire(ctx, s.CreationLockPath())
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := claimLock.Release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	if err := s.validateRepositoryHomeClaim(manifest); err != nil {
		return err
	}
	return s.SaveManifest(ctx, manifest)
}

func (s *Store) SaveManifest(ctx context.Context, manifest model.Manifest) (err error) {
	if !model.ValidID(manifest.ID) {
		return fmt.Errorf("invalid work item id %q", manifest.ID)
	}
	if err := normalizeManifest(&manifest); err != nil {
		return err
	}
	l, err := itemlock.Acquire(ctx, s.LockPath(manifest.ID))
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := l.Release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	if err := s.requireItem(manifest.ID); err != nil {
		return err
	}
	return writeManifestAtomic(s.ManifestPath(manifest.ID), manifest)
}

func (s *Store) LoadTerminalRuntime(id string) (*model.TerminalRuntime, error) {
	if !model.ValidID(id) {
		return nil, fmt.Errorf("invalid work item id %q", id)
	}
	b, err := os.ReadFile(s.TerminalRuntimePath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read terminal-runtime handle cache: %w", err)
	}
	var rt model.TerminalRuntime
	if err := json.Unmarshal(b, &rt); err != nil {
		return nil, fmt.Errorf("parse terminal-runtime handle cache %s: %w", s.TerminalRuntimePath(id), err)
	}
	return &rt, nil
}

func (s *Store) SaveTerminalRuntime(ctx context.Context, id string, workspace model.TerminalRuntime) (err error) {
	if !model.ValidID(id) {
		return fmt.Errorf("invalid work item id %q", id)
	}
	l, err := itemlock.Acquire(ctx, s.LockPath(id))
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := l.Release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	if err := s.requireItem(id); err != nil {
		return err
	}
	return writeJSONFileAtomic(s.TerminalRuntimePath(id), ".terminal-runtime-*.tmp", workspace, "terminal-runtime handle cache")
}

func (s *Store) RemoveTerminalRuntime(ctx context.Context, id string) (err error) {
	if !model.ValidID(id) {
		return fmt.Errorf("invalid work item id %q", id)
	}
	l, err := itemlock.Acquire(ctx, s.LockPath(id))
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := l.Release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	if err := os.Remove(s.TerminalRuntimePath(id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *Store) LoadAgentRuntime(id string) (*model.AgentRuntime, error) {
	if !model.ValidID(id) {
		return nil, fmt.Errorf("invalid work item id %q", id)
	}
	b, err := os.ReadFile(s.AgentRuntimePath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read agent runtime: %w", err)
	}
	var runtime model.AgentRuntime
	if err := json.Unmarshal(b, &runtime); err != nil {
		return nil, fmt.Errorf("parse agent runtime %s: %w", s.AgentRuntimePath(id), err)
	}
	if err := validateAgentRuntime(id, runtime); err != nil {
		return nil, fmt.Errorf("invalid agent runtime %s: %w", s.AgentRuntimePath(id), err)
	}
	return &runtime, nil
}

func (s *Store) SaveAgentRuntime(ctx context.Context, id string, runtime model.AgentRuntime) (err error) {
	if !model.ValidID(id) {
		return fmt.Errorf("invalid work item id %q", id)
	}
	runtime.WorkItemID = id
	if err := validateAgentRuntime(id, runtime); err != nil {
		return err
	}
	l, err := itemlock.Acquire(ctx, s.LockPath(id))
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := l.Release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	if err := s.requireItem(id); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.AgentRuntimePath(id)), 0o700); err != nil {
		return fmt.Errorf("create agent metadata directory: %w", err)
	}
	return writeJSONFileAtomic(s.AgentRuntimePath(id), ".agent-runtime-*.tmp", runtime, "agent runtime")
}

func validateRuntimeID(runtimeID string) error {
	runtimeID = strings.TrimSpace(runtimeID)
	if runtimeID == "" || runtimeID == "." || runtimeID == ".." || filepath.Base(runtimeID) != runtimeID {
		return fmt.Errorf("invalid agent runtime id %q", runtimeID)
	}
	return nil
}

func validateAgentRuntime(itemID string, runtime model.AgentRuntime) error {
	if err := validateRuntimeID(runtime.ID); err != nil {
		return err
	}
	if runtime.WorkItemID != "" && runtime.WorkItemID != itemID {
		return fmt.Errorf("work_item_id %q does not match %q", runtime.WorkItemID, itemID)
	}
	return nil
}

func (s *Store) AppendEvent(ctx context.Context, id string, event model.Event) (err error) {
	if !model.ValidID(id) {
		return fmt.Errorf("invalid work item id %q", id)
	}
	l, err := itemlock.Acquire(ctx, s.LockPath(id))
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := l.Release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	if err := s.requireItem(id); err != nil {
		return err
	}
	return appendEventNoLock(s.EventsPath(id), event)
}

func (s *Store) ReadEvents(id string) ([]model.Event, error) {
	f, err := os.Open(s.EventsPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open events: %w", err)
	}
	defer f.Close()

	var events []model.Event
	scanner := bufio.NewScanner(f)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		var ev model.Event
		if err := json.Unmarshal([]byte(text), &ev); err != nil {
			return nil, fmt.Errorf("parse events line %d: %w", line, err)
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}
	return events, nil
}

func (s *Store) ListManifests() ([]model.Manifest, []error) {
	entries, err := os.ReadDir(s.ItemsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{fmt.Errorf("read items directory: %w", err)}
	}
	var manifests []model.Manifest
	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		if !model.ValidID(id) {
			continue
		}
		m, err := s.LoadManifest(id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		manifests = append(manifests, m)
	}
	sort.SliceStable(manifests, func(i, j int) bool {
		if manifests[i].UpdatedAt.Equal(manifests[j].UpdatedAt) {
			return manifests[i].ID < manifests[j].ID
		}
		return manifests[i].UpdatedAt.After(manifests[j].UpdatedAt)
	})
	return manifests, errs
}

func (s *Store) descriptionText(m model.Manifest) string {
	path := filepath.Join(s.ItemDir(m.ID), model.DescriptionFilename)
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

type activeSlugMatches struct {
	normalized string
	exact      *model.Manifest
	substrings []model.Manifest
}

// matchActiveSlugs is the single slug-lookup policy used by strict slug
// commands and the broader work-item selector. Exact active slugs win;
// otherwise every active slug containing the normalized selector participates.
func matchActiveSlugs(manifests []model.Manifest, selector string) activeSlugMatches {
	normalized, valid := model.NormalizeSlugSelector(selector)
	matches := activeSlugMatches{normalized: normalized, substrings: []model.Manifest{}}
	if !valid {
		return matches
	}
	for _, manifest := range manifests {
		if manifest.State == model.StateArchived || manifest.Slug == "" {
			continue
		}
		if manifest.Slug == normalized {
			value := manifest
			matches.exact = &value
			matches.substrings = nil
			return matches
		}
		if strings.Contains(manifest.Slug, normalized) {
			matches.substrings = append(matches.substrings, manifest)
		}
	}
	sort.Slice(matches.substrings, func(i, j int) bool { return matches.substrings[i].Slug < matches.substrings[j].Slug })
	return matches
}

// ResolveActiveSlug resolves an exact canonical active slug or an unambiguous
// substring of one. Exact matches take precedence. It does not accept IDs,
// title/description keywords, labels, or archived aliases.
func (s *Store) ResolveActiveSlug(selector string) (model.Manifest, error) {
	manifests, errs := s.ListManifests()
	if len(errs) > 0 {
		return model.Manifest{}, errs[0]
	}
	return ResolveActiveSlugFromManifests(manifests, selector)
}

func ResolveActiveSlugFromManifests(manifests []model.Manifest, selector string) (model.Manifest, error) {
	matches := matchActiveSlugs(manifests, selector)
	if matches.normalized == "" {
		return model.Manifest{}, fmt.Errorf("invalid active slug substring %q", strings.TrimSpace(selector))
	}
	if matches.exact != nil {
		return *matches.exact, nil
	}
	if len(matches.substrings) == 1 {
		return matches.substrings[0], nil
	}
	if len(matches.substrings) > 1 {
		return model.Manifest{}, AmbiguousError{Selector: selector, Candidates: matches.substrings}
	}
	return model.Manifest{}, fmt.Errorf("active work item slug or substring %q not found", strings.TrimSpace(selector))
}

func (s *Store) Resolve(selector string) (model.Manifest, error) {
	manifests, errs := s.ListManifests()
	if len(errs) > 0 {
		return model.Manifest{}, errs[0]
	}
	return ResolveFromManifests(manifests, selector, s.descriptionText)
}

func ResolveFromManifests(manifests []model.Manifest, selector string, description func(model.Manifest) string) (model.Manifest, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return model.Manifest{}, ErrNotFound
	}
	upperSelector := strings.ToUpper(selector)

	for _, m := range manifests {
		if m.ID == upperSelector {
			return m, nil
		}
	}
	var prefix []model.Manifest
	for _, m := range manifests {
		if strings.HasPrefix(m.ID, upperSelector) {
			prefix = append(prefix, m)
		}
	}
	if len(prefix) == 1 {
		return prefix[0], nil
	}
	if len(prefix) > 1 {
		return model.Manifest{}, AmbiguousError{Selector: selector, Candidates: prefix}
	}

	slugMatches := matchActiveSlugs(manifests, selector)
	if slugMatches.exact != nil {
		return *slugMatches.exact, nil
	}

	var title []model.Manifest
	for _, m := range manifests {
		if m.State != model.StateArchived && strings.EqualFold(m.Title, selector) {
			title = append(title, m)
		}
	}
	if len(title) == 1 {
		return title[0], nil
	}
	if len(title) > 1 {
		return model.Manifest{}, AmbiguousError{Selector: selector, Candidates: title}
	}

	// A slug-shaped selector is more specific than description, title-substring,
	// or label search. Do not let incidental references in another item's
	// description make one unique slug substring ambiguous.
	if len(slugMatches.substrings) == 1 {
		return slugMatches.substrings[0], nil
	}
	if len(slugMatches.substrings) > 1 {
		return model.Manifest{}, AmbiguousError{Selector: selector, Candidates: slugMatches.substrings}
	}

	partialSelector := strings.ToLower(selector)
	partial := []model.Manifest{}
	seen := map[string]bool{}
	for _, m := range manifests {
		if m.State == model.StateArchived {
			continue
		}
		matched := false
		if partialSelector != "" && strings.Contains(strings.ToLower(m.Title), partialSelector) {
			matched = true
		}
		if !matched && description != nil && partialSelector != "" && strings.Contains(strings.ToLower(description(m)), partialSelector) {
			matched = true
		}
		if !matched {
			for _, label := range m.Labels {
				if slugMatches.normalized != "" && strings.Contains(label, slugMatches.normalized) {
					matched = true
					break
				}
			}
		}
		if matched && !seen[m.ID] {
			seen[m.ID] = true
			partial = append(partial, m)
		}
	}
	if len(partial) == 1 {
		return partial[0], nil
	}
	if len(partial) > 1 {
		return model.Manifest{}, AmbiguousError{Selector: selector, Candidates: partial}
	}
	return model.Manifest{}, ErrNotFound
}

func (s *Store) FindByWorktree(path string) (model.Manifest, error) {
	manifests, errs := s.ListManifests()
	if len(errs) > 0 {
		return model.Manifest{}, errs[0]
	}
	return FindByWorktreeFromManifests(manifests, path)
}

func FindByWorktreeFromManifests(manifests []model.Manifest, path string) (model.Manifest, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return model.Manifest{}, err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
	}
	var matches []model.Manifest
	for _, m := range manifests {
		if m.Checkout.Path == nil {
			continue
		}
		checkoutAbs, err := filepath.Abs(*m.Checkout.Path)
		if err != nil {
			continue
		}
		if resolved, resolveErr := filepath.EvalSymlinks(checkoutAbs); resolveErr == nil {
			checkoutAbs = resolved
		}
		if abs == checkoutAbs || isSubpath(abs, checkoutAbs) {
			matches = append(matches, m)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return model.Manifest{}, AmbiguousError{Selector: path, Candidates: matches}
	}
	return model.Manifest{}, ErrNotFound
}

func isSubpath(path, parent string) bool {
	rel, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func appendEventNoLock(path string, event model.Event) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open events file: %w", err)
	}
	defer f.Close()
	b, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync events file: %w", err)
	}
	return nil
}

func writeBytesAtomic(path, pattern string, value []byte, description string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s directory: %w", description, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), pattern)
	if err != nil {
		return fmt.Errorf("create temporary %s: %w", description, err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod %s temp: %w", description, err)
	}
	if _, err := tmp.Write(value); err != nil {
		return fmt.Errorf("write %s temp: %w", description, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("fsync %s temp: %w", description, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s temp: %w", description, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s: %w", description, err)
	}
	cleanup = false
	return fsyncDir(filepath.Dir(path))
}

func writeJSONFileAtomic(path, pattern string, value any, description string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s directory: %w", description, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), pattern)
	if err != nil {
		return fmt.Errorf("create temporary %s: %w", description, err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("encode %s: %w", description, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod %s temp: %w", description, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fsync %s temp: %w", description, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s temp: %w", description, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s: %w", description, err)
	}
	cleanup = false
	return fsyncDir(filepath.Dir(path))
}

func writeManifestAtomic(path string, manifest model.Manifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create manifest directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".manifest-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary manifest: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(manifest); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("encode manifest: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod manifest temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fsync manifest temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close manifest temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename manifest: %w", err)
	}
	cleanup = false
	if err := fsyncDir(filepath.Dir(path)); err != nil {
		return err
	}
	return nil
}

func fsyncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory for fsync: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		// Some filesystems reject directory fsync. Linux filesystems used for durable state should support it,
		// so return the error rather than silently downgrading durability.
		return fmt.Errorf("fsync directory %s: %w", path, err)
	}
	return nil
}
