package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/regb/workitem/internal/itemnotes"
	"github.com/regb/workitem/internal/model"
)

// attachedCopy is an untracked file in the checkout that is unchanged since
// it was attached, under Name.
type attachedCopy struct {
	File, Name string
}

// attachedCopies splits the checkout's untracked files into unchanged
// copies of attachments, whose content the item keeps, and the rest.
func (a *App) attachedCopies(ctx context.Context, m model.Manifest) ([]attachedCopy, []string, error) {
	if m.Checkout.Claim == nil || m.Checkout.Claim.Path == "" || a.Git == nil {
		return nil, nil, nil
	}
	root := m.Checkout.Claim.Path
	untracked, err := a.Git.UntrackedFiles(ctx, root)
	if err != nil {
		return nil, nil, fmt.Errorf("list untracked files: %w", err)
	}
	notes, err := itemnotes.Read(a.Store.ItemDir(m.ID))
	if err != nil {
		return nil, nil, fmt.Errorf("read item notes: %w", err)
	}
	latest := map[string]itemnotes.Attachment{}
	for _, note := range notes {
		if attached := note.Attachment; attached != nil && attached.Path != "" && itemnotes.ValidPath(attached.Path) == nil {
			latest[attached.Path] = *attached
		}
	}
	copies, rest := []attachedCopy{}, []string{}
	for _, file := range untracked {
		attached, ok := latest[file]
		same := false
		if ok {
			if same, err = sameContent(filepath.Join(root, filepath.FromSlash(file)), attached); err != nil {
				return nil, nil, err
			}
		}
		if same {
			copies = append(copies, attachedCopy{File: file, Name: attached.Name})
		} else {
			rest = append(rest, file)
		}
	}
	return copies, rest, nil
}

// removeAttachedCopies removes the untracked files in an item's checkout
// that are unchanged since they were attached: a file whose content is the
// latest attachment from its path. The attachment keeps that content, so
// releasing the checkout loses nothing. Edited files and anything else stay
// and still count as uncommitted changes. It returns what it removed.
func (a *App) removeAttachedCopies(ctx context.Context, m model.Manifest) ([]string, error) {
	copies, _, err := a.attachedCopies(ctx, m)
	if err != nil || len(copies) == 0 {
		return nil, err
	}
	root := m.Checkout.Claim.Path
	removed := []string{}
	for _, found := range copies {
		full := filepath.Join(root, filepath.FromSlash(found.File))
		if err := os.Remove(full); err != nil {
			return removed, fmt.Errorf("remove %s: %w", found.File, err)
		}
		removeEmptyParents(root, filepath.Dir(full))
		removed = append(removed, fmt.Sprintf("removed %s from the checkout, unchanged since it was attached as %s", found.File, found.Name))
	}
	return removed, nil
}

// UncommittedChanges is what releasing an item's checkout would find:
// Files are uncommitted changes, which block or get committed; Attached are
// unchanged copies of attachments, which are removed.
type UncommittedChanges struct {
	Files    []string `json:"files"`
	Attached []string `json:"attached"`
}

// ItemUncommittedChanges reads, without changing anything, what is
// uncommitted in an item's checkout.
func (a *App) ItemUncommittedChanges(ctx context.Context, opts ResolveOptions) (UncommittedChanges, error) {
	m, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return UncommittedChanges{Files: []string{}, Attached: []string{}}, err
	}
	return a.uncommittedChanges(ctx, m)
}

func (a *App) uncommittedChanges(ctx context.Context, m model.Manifest) (UncommittedChanges, error) {
	result := UncommittedChanges{Files: []string{}, Attached: []string{}}
	if m.Checkout.Claim == nil || m.Checkout.Claim.Path == "" || a.Git == nil {
		return result, nil
	}
	status, err := a.Git.StatusPorcelain(ctx, m.Checkout.Claim.Path)
	if err != nil {
		return result, fmt.Errorf("could not inspect checkout status: %w", err)
	}
	for _, line := range strings.Split(status, "\n") {
		if len(line) > 3 && !strings.HasPrefix(line, "??") {
			result.Files = append(result.Files, line[3:])
		}
	}
	copies, rest, err := a.attachedCopies(ctx, m)
	if err != nil {
		return result, err
	}
	result.Files = append(result.Files, rest...)
	for _, found := range copies {
		result.Attached = append(result.Attached, found.File)
	}
	return result, nil
}

// sameContent reports whether the regular file at path holds exactly the
// attachment's content.
func sameContent(path string, attached itemnotes.Attachment) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() != attached.Size {
		return false, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:]) == attached.SHA256, nil
}

// removeEmptyParents removes dir and its parents up to root while they are
// empty, so a folder that held only attached copies goes too.
func removeEmptyParents(root, dir string) {
	root = filepath.Clean(root)
	for dir = filepath.Clean(dir); dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil {
			return
		}
	}
}
