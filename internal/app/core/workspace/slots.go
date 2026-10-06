package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/regb/workitem/internal/model"
)

type slotAssignment struct {
	Manifest model.Manifest
	Source   string
	Warnings []string
}

type CheckoutRelease struct {
	Manifest model.Manifest
	LastHead string
	Dirty    bool
	Slot     bool
	Warnings []string
}

func (s *Service) assignSlot(ctx context.Context, m model.Manifest) (slotAssignment, error) {
	branch := strings.TrimSpace(m.Checkout.Specification.Branch)
	if branch == "" {
		return slotAssignment{}, fmt.Errorf("cannot assign checkout: implementation branch is missing")
	}
	startPoint := strings.TrimSpace(m.Repository.CreatedFromCommit)
	if startPoint == "" {
		return slotAssignment{}, fmt.Errorf("cannot assign checkout: no last observed HEAD or created-from commit recorded")
	}
	repoKey := repositoryKey(m.Repository)
	slotsRoot := filepath.Join(s.store.WorktreesDir(), repoKey)
	if err := os.MkdirAll(slotsRoot, 0o700); err != nil {
		return slotAssignment{}, fmt.Errorf("create slots directory: %w", err)
	}
	assigned, err := s.assignedCheckoutPaths(m.ID)
	if err != nil {
		return slotAssignment{}, err
	}
	slots, maxIndex, err := listSlots(slotsRoot)
	if err != nil {
		return slotAssignment{}, err
	}
	warnings := []string{}
	for _, slot := range slots {
		if assigned[slot.Path] {
			continue
		}
		status, err := s.git.StatusPorcelain(ctx, slot.Path)
		if err != nil {
			warnings = append(warnings, "slot "+filepath.Base(slot.Path)+" is unavailable: "+err.Error())
			continue
		}
		if strings.TrimSpace(status) != "" {
			warnings = append(warnings, "slot "+filepath.Base(slot.Path)+" is dirty; skipped")
			continue
		}
		source, err := s.switchExistingSlot(ctx, m, slot.Path, branch, startPoint)
		if err != nil {
			warnings = append(warnings, "slot "+filepath.Base(slot.Path)+" could not be assigned: "+err.Error())
			continue
		}
		return s.finishSlotAssignment(ctx, m, slot.Path, branch, source, warnings, true)
	}

	path := filepath.Join(slotsRoot, fmt.Sprintf("slot-%04d", maxIndex+1))
	source, err := s.createSlot(ctx, m, path, branch, startPoint)
	if err != nil {
		return slotAssignment{}, err
	}
	return s.finishSlotAssignment(ctx, m, path, branch, source, warnings, false)
}

func (s *Service) finishSlotAssignment(ctx context.Context, m model.Manifest, path, branch, source string, warnings []string, reused bool) (slotAssignment, error) {
	if reused {
		if revoked, err := s.revokeReusedSlotDirenvTrust(ctx, path); err != nil {
			return slotAssignment{}, fmt.Errorf("revoke direnv trust for reused slot before assignment: %w", err)
		} else if revoked {
			warnings = append(warnings, "revoked prior direnv trust for reused slot; runtime startup will request approval again")
		}
	}
	if _, err := m.Checkout.AssignNewClaim(m.Checkout.Specification.Executor, repositoryKey(m.Repository), filepath.Base(path), path); err != nil {
		return slotAssignment{}, err
	}
	return slotAssignment{Manifest: m, Source: source, Warnings: warnings}, nil
}

func (s *Service) switchExistingSlot(ctx context.Context, m model.Manifest, path, branch, startPoint string) (string, error) {
	exists, err := s.git.BranchExists(ctx, m.Repository.OperationalRoot(), branch)
	if err != nil {
		return "", err
	}
	if exists {
		if err := s.git.Switch(ctx, path, branch, "", false); err != nil {
			return "", err
		}
		return filepath.Base(path) + " branch " + branch, nil
	}
	if err := s.git.Switch(ctx, path, branch, startPoint, true); err != nil {
		return "", err
	}
	return filepath.Base(path) + " new branch " + branch + " from " + startPoint, nil
}

func (s *Service) createSlot(ctx context.Context, m model.Manifest, path, branch, startPoint string) (string, error) {
	exists, err := s.git.BranchExists(ctx, m.Repository.OperationalRoot(), branch)
	if err != nil {
		return "", err
	}
	if exists {
		if err := s.git.WorktreeAdd(ctx, model.WorktreeAddOptions{RepoRoot: m.Repository.OperationalRoot(), Path: path, Branch: branch}); err != nil {
			return "", fmt.Errorf("create slot from branch: %w", err)
		}
		return filepath.Base(path) + " branch " + branch, nil
	}
	if err := s.git.WorktreeAdd(ctx, model.WorktreeAddOptions{RepoRoot: m.Repository.OperationalRoot(), Path: path, Branch: branch, StartPoint: startPoint, NewBranch: true}); err != nil {
		return "", fmt.Errorf("create slot from %s: %w", startPoint, err)
	}
	return filepath.Base(path) + " new branch " + branch + " from " + startPoint, nil
}

func (s *Service) assignedCheckoutPaths(excludeID string) (map[string]bool, error) {
	items, errs := s.store.ListManifests()
	if len(errs) > 0 {
		return nil, errs[0]
	}
	assigned := map[string]bool{}
	for _, item := range items {
		if item.ID == excludeID || !item.Checkout.Present() || item.Checkout.Claim == nil || item.Checkout.Claim.Path == "" {
			continue
		}
		abs, err := filepath.Abs(item.Checkout.Claim.Path)
		if err != nil {
			continue
		}
		assigned[abs] = true
	}
	return assigned, nil
}

type slotInfo struct {
	Index int
	Path  string
}

func listSlots(root string) ([]slotInfo, int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	slots := []slotInfo{}
	maxIndex := 0
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "slot-") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(entry.Name(), "slot-"))
		if err != nil || n <= 0 {
			continue
		}
		if n > maxIndex {
			maxIndex = n
		}
		slots = append(slots, slotInfo{Index: n, Path: filepath.Join(root, entry.Name())})
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].Index < slots[j].Index })
	return slots, maxIndex, nil
}

func repositoryKey(repo model.Repository) string { return model.RepositoryKey(repo) }

func expectedCheckoutBranch(m model.Manifest) string {
	return strings.TrimSpace(m.Checkout.Specification.Branch)
}

func (s *Service) ensureCheckoutBranch(ctx context.Context, m model.Manifest) error {
	if !m.Checkout.Present() || m.Checkout.Claim == nil || m.Checkout.Claim.Path == "" {
		return nil
	}
	path := m.Checkout.Claim.Path
	expected := expectedCheckoutBranch(m)
	current, err := s.git.CurrentBranch(ctx, path)
	if err != nil {
		return fmt.Errorf("could not inspect checkout branch: %w", err)
	}
	if current == "" {
		return fmt.Errorf("checkout at %s is detached; expected branch %s", path, expected)
	}
	if current != expected {
		return fmt.Errorf("checkout branch mismatch at %s: expected %s, found %s; repair the worktree before continuing", path, expected, current)
	}
	return nil
}

func (s *Service) releaseCheckout(ctx context.Context, m model.Manifest, force bool) (CheckoutRelease, error) {
	if !m.Checkout.Present() || m.Checkout.Claim == nil || m.Checkout.Claim.Path == "" {
		return CheckoutRelease{Manifest: m}, nil
	}
	if m.Checkout.Specification.Kind == model.WorkspaceKindRepositoryHome {
		if force {
			if _, err := os.Stat(m.Checkout.Claim.Path); os.IsNotExist(err) {
				m.Checkout.ClearClaim()
				return CheckoutRelease{Manifest: m, Slot: false, Warnings: []string{"missing repository-home checkout required no filesystem changes; released only its logical work-item claim"}}, nil
			}
		}
		if err := s.ensureCheckoutBranch(ctx, m); err != nil {
			return CheckoutRelease{}, err
		}
		head, err := s.git.Head(ctx, m.Checkout.Claim.Path)
		if err != nil {
			return CheckoutRelease{}, fmt.Errorf("could not read repository-home HEAD: %w", err)
		}
		m.Checkout.ClearClaim()
		return CheckoutRelease{Manifest: m, LastHead: head, Slot: false, Warnings: []string{"repository-home checkout was left untouched; released only its logical work-item claim"}}, nil
	}
	path := m.Checkout.Claim.Path
	if err := s.validateManagedCheckoutPath(path); err != nil {
		return CheckoutRelease{}, err
	}
	if err := s.ensureCheckoutBranch(ctx, m); err != nil {
		return CheckoutRelease{}, err
	}
	warnings := []string{}
	lastHead := ""
	if head, err := s.git.Head(ctx, path); err == nil && strings.TrimSpace(head) != "" {
		lastHead = head
	} else if err != nil {
		warnings = append(warnings, "could not read checkout HEAD: "+err.Error())
	}
	status, statusErr := s.git.StatusPorcelain(ctx, path)
	if statusErr != nil {
		return CheckoutRelease{}, fmt.Errorf("could not inspect checkout status: %w", statusErr)
	}
	dirty := strings.TrimSpace(status) != ""
	if dirty {
		return CheckoutRelease{}, fmt.Errorf("checkout has uncommitted changes; commit, stash, or clean before releasing the slot")
	}
	slot := s.isSlotPath(m.Repository, path)
	if !slot {
		return CheckoutRelease{}, fmt.Errorf("managed checkout %s is outside the slot directory", path)
	}
	m.Checkout.ClearClaim()
	return CheckoutRelease{Manifest: m, LastHead: lastHead, Dirty: dirty, Slot: slot, Warnings: warnings}, nil
}

func (s *Service) validateManagedCheckoutPath(path string) error {
	root, err := filepath.Abs(s.store.WorktreesDir())
	if err != nil {
		return err
	}
	got, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if got != root && !strings.HasPrefix(got, root+string(filepath.Separator)) {
		return fmt.Errorf("refusing to release unmanaged checkout path %q; expected under %q", got, root)
	}
	return nil
}

func IsSlotPathName(path string) bool {
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "slot-") {
		return false
	}
	_, err := strconv.Atoi(strings.TrimPrefix(base, "slot-"))
	return err == nil
}

func (s *Service) isSlotPath(repo model.Repository, path string) bool {
	root := filepath.Join(s.store.WorktreesDir(), repositoryKey(repo))
	got, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	want, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	if filepath.Dir(got) != want {
		return false
	}
	return IsSlotPathName(got)
}
