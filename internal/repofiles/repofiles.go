// Package repofiles keeps a registered repository's files: the untracked
// files its checkouts need, such as a .env, which wi manages instead of Git.
// Each file is stored at the path it takes in a checkout. Content stays
// private and outside the bare store; only the coordinator writes it.
package repofiles

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// MaxBytes bounds one file: configuration and certificates, not data.
	MaxBytes = 4 << 20
	// MaxFiles bounds a repository's files.
	MaxFiles = 200
)

// File describes a stored file. It never carries content.
type File struct {
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ValidPath accepts a relative, slash-separated path inside a checkout,
// outside its Git metadata.
func ValidPath(p string) error {
	if p == "" || len(p) > 512 {
		return errors.New("file path must be 1 to 512 bytes")
	}
	if strings.ContainsAny(p, "\\\x00\n\r") || strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return fmt.Errorf("file path %q must be a clean relative path", p)
	}
	for _, part := range strings.Split(p, "/") {
		if part == "." || part == ".." || part == ".git" {
			return fmt.Errorf("file path %q must stay inside the checkout and outside .git", p)
		}
	}
	return nil
}

// List returns the files under dir, sorted by path. A missing dir holds none.
func List(dir string) ([]File, error) {
	files := []File{}
	err := filepath.WalkDir(dir, func(full string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && full == dir {
				return fs.SkipAll
			}
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(dir, full)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if strings.HasPrefix(path.Base(relative), ".wi-staged-") {
			return nil
		}
		if !entry.Type().IsRegular() || ValidPath(relative) != nil {
			return fmt.Errorf("unexpected entry %s in repository files", relative)
		}
		content, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files = append(files, File{Path: relative, Size: int64(len(content)), SHA256: Digest(content), UpdatedAt: info.ModTime().UTC()})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, err
}

// Read returns a stored file's content.
func Read(dir, p string) ([]byte, error) {
	if err := ValidPath(p); err != nil {
		return nil, err
	}
	full := filepath.Join(dir, filepath.FromSlash(p))
	if info, err := os.Lstat(full); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no repository file %s", p)
		}
		return nil, err
	} else if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("repository file %s is not a regular file", p)
	}
	return os.ReadFile(full)
}

// Put stores content at p, replacing what was there, atomically and
// privately.
func Put(dir, p string, content []byte) (File, error) {
	if err := ValidPath(p); err != nil {
		return File{}, err
	}
	if len(content) > MaxBytes {
		return File{}, fmt.Errorf("file is %d bytes; the limit is %d", len(content), MaxBytes)
	}
	existing, err := List(dir)
	if err != nil {
		return File{}, err
	}
	known := false
	for _, file := range existing {
		if file.Path == p {
			known = true
		}
		// A file and a directory cannot share a path.
		if strings.HasPrefix(file.Path, p+"/") || strings.HasPrefix(p, file.Path+"/") {
			return File{}, fmt.Errorf("%s conflicts with stored file %s", p, file.Path)
		}
	}
	if !known && len(existing) >= MaxFiles {
		return File{}, fmt.Errorf("a repository holds at most %d files", MaxFiles)
	}
	full := filepath.Join(dir, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return File{}, err
	}
	if err := WriteFile(full, content); err != nil {
		return File{}, err
	}
	return File{Path: p, Size: int64(len(content)), SHA256: Digest(content), UpdatedAt: time.Now().UTC()}, nil
}

// Remove deletes a stored file and the directories it leaves empty.
func Remove(dir, p string) error {
	if err := ValidPath(p); err != nil {
		return err
	}
	full := filepath.Join(dir, filepath.FromSlash(p))
	if err := os.Remove(full); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("no repository file %s", p)
		}
		return err
	}
	for parent := filepath.Dir(full); parent != dir && strings.HasPrefix(parent, dir); parent = filepath.Dir(parent) {
		if os.Remove(parent) != nil {
			break
		}
	}
	return nil
}

// WriteFile replaces full with content through a private staged file, so a
// reader never sees a partial file.
func WriteFile(full string, content []byte) error {
	staged, err := os.CreateTemp(filepath.Dir(full), ".wi-staged-*")
	if err != nil {
		return err
	}
	name := staged.Name()
	defer os.Remove(name)
	if _, err := staged.Write(content); err != nil {
		staged.Close()
		return err
	}
	if err := staged.Chmod(0o600); err != nil {
		staged.Close()
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	return os.Rename(name, full)
}

// Digest is the hex SHA-256 of content.
func Digest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
