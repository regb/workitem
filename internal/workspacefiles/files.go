// Package workspacefiles reads bounded source and replaces existing files beneath
// a checkout. Directory descriptors and no-follow opens prevent symlink escapes.
package workspacefiles

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/regb/workitem/internal/apperr"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const MaxBytes = 1 << 20

// ErrConflict means the file changed since the caller read it.
var ErrConflict = apperr.Stalef("file changed since it was opened; reload before saving")

// ErrInvalidPath refuses a path that is not a clean relative path outside Git
// metadata.
var ErrInvalidPath = errors.New("invalid file path")

// ErrNotEditable refuses a file that is not regular UTF-8 text within the
// size limit.
var ErrNotEditable = errors.New("file is not editable text")
var errMissingParent = errors.New("file directory does not exist")

type File struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	SHA256   string `json:"sha256"`
	Exists   bool   `json:"exists"`
	Binary   bool   `json:"binary"`
	TooLarge bool   `json:"too_large"`
}

func ValidPath(p string) error {
	if p == "" || !utf8.ValidString(p) || strings.ContainsAny(p, "\x00\\") || strings.HasPrefix(p, "/") || path.Clean(p) != p || p == "." {
		return fmt.Errorf("%w: must be a clean relative path", ErrInvalidPath)
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || strings.EqualFold(part, ".git") {
			return fmt.Errorf("%w: Git metadata and traversal are forbidden", ErrInvalidPath)
		}
	}
	return nil
}

func Source(p string, data []byte) File {
	f := File{Path: p, Exists: true}
	if len(data) > MaxBytes {
		f.TooLarge = true
		return f
	}
	sum := sha256.Sum256(data)
	f.SHA256 = hex.EncodeToString(sum[:])
	f.Binary = !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0
	if !f.Binary {
		f.Content = string(data)
	}
	return f
}

// parent pins each directory with O_NOFOLLOW. No component may be a symlink,
// including links pointing back inside the checkout or into its Git metadata.
func parent(root, p string) (*os.File, string, error) {
	if err := ValidPath(p); err != nil {
		return nil, "", err
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", err
	}
	dir := os.NewFile(uintptr(fd), root)
	parts := strings.Split(p, "/")
	for _, part := range parts[:len(parts)-1] {
		fd, err := unix.Openat(int(dir.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		dir.Close()
		if errors.Is(err, unix.ENOENT) {
			return nil, "", errMissingParent
		}
		if err != nil {
			return nil, "", fmt.Errorf("open file directory without symlinks: %w", err)
		}
		dir = os.NewFile(uintptr(fd), part)
	}
	return dir, parts[len(parts)-1], nil
}

func readAt(dir *os.File, name, p string) (File, os.FileInfo, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return File{Path: p}, nil, nil
	}
	if err != nil {
		return File{}, nil, fmt.Errorf("open regular file without symlinks: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return File{}, nil, err
	}
	if !info.Mode().IsRegular() {
		return File{}, nil, fmt.Errorf("%w: only regular files can be viewed or edited", ErrNotEditable)
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err != nil {
		return File{}, nil, err
	}
	return Source(p, data), info, nil
}

func Read(root, p string) (File, error) {
	dir, name, err := parent(root, p)
	if errors.Is(err, errMissingParent) {
		return File{Path: p}, nil
	}
	if err != nil {
		return File{}, err
	}
	defer dir.Close()
	f, _, err := readAt(dir, name, p)
	return f, err
}

// Save checks content twice and atomically replaces the directory entry. Wi
// serializes its own saves, but external writers do not take its locks, so this
// is optimistic conflict detection, not a filesystem-wide compare-and-swap.
func Save(root, p, content, expected string) (File, error) {
	if strings.TrimSpace(expected) == "" {
		return File{}, fmt.Errorf("%w: the SHA-256 of the content read is required", ErrConflict)
	}
	next := Source(p, []byte(content))
	if next.Binary || next.TooLarge {
		return File{}, fmt.Errorf("%w: only UTF-8 text up to 1 MiB can be saved", ErrNotEditable)
	}
	dir, name, err := parent(root, p)
	if err != nil {
		return File{}, err
	}
	defer dir.Close()
	old, info, err := readAt(dir, name, p)
	if err != nil {
		return File{}, err
	}
	if !old.Exists || old.SHA256 != expected {
		return File{}, ErrConflict
	}
	if old.Binary || old.TooLarge {
		return File{}, ErrNotEditable
	}
	if old.SHA256 == next.SHA256 {
		return next, nil
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return File{}, err
	}
	tmp := ".wi-save-" + hex.EncodeToString(random[:])
	fd, err := unix.Openat(int(dir.Fd()), tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return File{}, err
	}
	defer unix.Unlinkat(int(dir.Fd()), tmp, 0)
	out := os.NewFile(uintptr(fd), tmp)
	_, err = out.WriteString(content)
	if err == nil {
		err = out.Chmod(info.Mode().Perm())
	}
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err != nil {
		return File{}, err
	}
	if closeErr != nil {
		return File{}, closeErr
	}
	current, currentInfo, err := readAt(dir, name, p)
	if err != nil {
		return File{}, err
	}
	if current.SHA256 != expected || currentInfo == nil || !os.SameFile(info, currentInfo) {
		return File{}, ErrConflict
	}
	if err := unix.Renameat(int(dir.Fd()), tmp, int(dir.Fd()), name); err != nil {
		return File{}, err
	}
	return next, nil
}
