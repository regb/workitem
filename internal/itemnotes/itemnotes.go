// Package itemnotes keeps a work item's notes and attachments: an
// append-only log of short progress notes, and named files stored by content
// beside it. Both are canonical filesystem content in the item directory; the
// coordinator is their only writer.
package itemnotes

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	NotesFilename      = "notes.jsonl"
	AttachmentsDirname = "attachments"
	// MaxNoteBytes keeps notes concise; longer content belongs in an
	// attachment.
	MaxNoteBytes = 2048
	// MaxAttachmentBytes fits one attachment, encoded, in a coordinator
	// request.
	MaxAttachmentBytes = 16 << 20
	maxNameBytes       = 128
	// maxLogBytes bounds what a read of the log accepts.
	maxLogBytes = 64 << 20
)

// Note is one entry of an item's log. An attachment's entry names the
// content attached; Head is the item worktree's commit when the note was
// taken, if it had one.
type Note struct {
	Time       time.Time   `json:"time"`
	Text       string      `json:"text"`
	Head       string      `json:"head,omitempty"`
	Attachment *Attachment `json:"attachment,omitempty"`
}

type Attachment struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Type   string `json:"type"`
}

// Current is an attachment name's latest content.
type Current struct {
	Attachment
	Time time.Time `json:"time"`
}

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// CleanText trims a note and checks it is a short, printable text.
func CleanText(text string) (string, error) {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	switch {
	case text == "":
		return "", errors.New("note is empty")
	case len(text) > MaxNoteBytes:
		return "", fmt.Errorf("note is %d bytes; notes are at most %d, attach longer content", len(text), MaxNoteBytes)
	case !utf8.ValidString(text):
		return "", errors.New("note is not valid UTF-8")
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return "", errors.New("note contains control characters")
		}
	}
	return text, nil
}

// ValidName reports whether name is a plain file name for an attachment.
func ValidName(name string) error {
	switch {
	case name == "" || len(name) > maxNameBytes:
		return fmt.Errorf("attachment name must be 1 to %d bytes", maxNameBytes)
	case !utf8.ValidString(name) || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") || strings.TrimSpace(name) != name:
		return fmt.Errorf("attachment name %q must be a plain file name", name)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("attachment name %q contains control characters", name)
		}
	}
	return nil
}

// ContentType names an attachment's media type from its name, or else its
// content.
func ContentType(name string, content []byte) string {
	if value := mime.TypeByExtension(filepath.Ext(name)); value != "" {
		return value
	}
	if strings.EqualFold(filepath.Ext(name), ".md") {
		return "text/markdown; charset=utf-8"
	}
	return http.DetectContentType(content)
}

// Read returns an item's notes, oldest first. A missing log is empty; a
// final line cut short by an interrupted append is ignored.
func Read(itemDir string) ([]Note, error) {
	file, err := openRegular(filepath.Join(itemDir, NotesFilename))
	if errors.Is(err, os.ErrNotExist) {
		return []Note{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := bufio.NewReader(io.LimitReader(file, maxLogBytes))
	notes := []Note{}
	for line := 1; ; line++ {
		data, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			return notes, nil
		}
		if err != nil {
			return nil, err
		}
		var note Note
		if err := json.Unmarshal(data, &note); err != nil {
			return nil, fmt.Errorf("read notes: line %d: %w", line, err)
		}
		notes = append(notes, note)
	}
}

// Attachments returns each attachment name's latest content, by name.
func Attachments(notes []Note) []Current {
	latest := map[string]Current{}
	for _, note := range notes {
		if note.Attachment != nil {
			latest[note.Attachment.Name] = Current{Attachment: *note.Attachment, Time: note.Time}
		}
	}
	current := make([]Current, 0, len(latest))
	for _, attachment := range latest {
		current = append(current, attachment)
	}
	sort.Slice(current, func(i, j int) bool { return current[i].Name < current[j].Name })
	return current
}

// Find returns the latest content attached under name.
func Find(notes []Note, name string) (Current, bool) {
	for _, attachment := range Attachments(notes) {
		if attachment.Name == name {
			return attachment, true
		}
	}
	return Current{}, false
}

// Append adds a note to the log. Only the coordinator calls it, one at a
// time.
func Append(itemDir string, note Note) error {
	if err := requireItemDir(itemDir); err != nil {
		return err
	}
	data, err := json.Marshal(note)
	if err != nil {
		return err
	}
	path := filepath.Join(itemDir, NotesFilename)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("notes log %s is not a regular file", path)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := dropTornLine(file); err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

// dropTornLine removes the end of an append that was cut short: a last line
// without its newline, which no reader has accepted as a note.
func dropTornLine(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	end := info.Size()
	buffer := make([]byte, 4096)
	for at := end; at > 0; {
		n := int64(len(buffer))
		if at < n {
			n = at
		}
		at -= n
		if _, err := file.ReadAt(buffer[:n], at); err != nil {
			return err
		}
		if index := bytes.LastIndexByte(buffer[:n], '\n'); index >= 0 {
			if keep := at + int64(index) + 1; keep < end {
				return file.Truncate(keep)
			}
			return nil
		}
	}
	return file.Truncate(0)
}

// WriteBlob stores content by its digest and returns the digest. Content
// already stored is kept as it is.
func WriteBlob(itemDir string, content []byte) (string, error) {
	if len(content) > MaxAttachmentBytes {
		return "", fmt.Errorf("attachment is %d bytes; the limit is %d", len(content), MaxAttachmentBytes)
	}
	if err := requireItemDir(itemDir); err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	dir := filepath.Join(itemDir, AttachmentsDirname)
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if info, err := os.Lstat(dir); err != nil {
		return "", err
	} else if !info.IsDir() {
		return "", fmt.Errorf("attachments %s is not a directory", dir)
	}
	path := filepath.Join(dir, digest)
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, content) {
		return digest, nil
	}
	temp, err := os.CreateTemp(dir, ".attachment-*.tmp")
	if err != nil {
		return "", err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return "", err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return "", err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return digest, nil
}

// OpenBlob opens stored content by its digest.
func OpenBlob(itemDir, digest string) (*os.File, error) {
	if !digestPattern.MatchString(digest) {
		return nil, fmt.Errorf("invalid attachment digest %q", digest)
	}
	if info, err := os.Lstat(filepath.Join(itemDir, AttachmentsDirname)); err != nil {
		return nil, err
	} else if !info.IsDir() {
		return nil, errors.New("attachments is not a directory")
	}
	return openRegular(filepath.Join(itemDir, AttachmentsDirname, digest))
}

func requireItemDir(itemDir string) error {
	info, err := os.Lstat(itemDir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("item directory %s is not a directory", itemDir)
	}
	return nil
}

// openRegular opens a file only if it is a regular file, not a link.
func openRegular(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return os.Open(path)
}
