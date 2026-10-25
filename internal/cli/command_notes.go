package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/itemnotes"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

func runNote(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("note", cfg.Stderr)
	var item string
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	text := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(text) == "" {
		return usageErr{errors.New("note text is required")}
	}
	if _, err := itemnotes.CleanText(text); err != nil {
		return usageErr{err}
	}
	m, err := cfg.App.ResolveItem(ctx, appResolve(cfg, item))
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	note, err := cfg.Coordinator.AddNote(requestCtx, coordinator.AddNoteRequest{ItemID: m.ID, Text: text})
	cancel()
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, note)
	}
	fmt.Fprintf(cfg.Stdout, "noted on %s\n", m.ID)
	return nil
}

func runNotes(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	m, notes, err := readItemNotes(ctx, "notes", args, cfg)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, struct {
			WorkItemID string           `json:"work_item_id"`
			Notes      []itemnotes.Note `json:"notes"`
		}{m.ID, notes})
	}
	for _, note := range notes {
		line := note.Time.Local().Format("2006-01-02 15:04")
		if note.Head != "" {
			line += "  " + shortSHA(note.Head)
		}
		text := strings.ReplaceAll(note.Text, "\n", "\n    ")
		if note.Attachment != nil && !strings.Contains(note.Text, note.Attachment.Name) {
			text += " [" + note.Attachment.Name + "]"
		}
		fmt.Fprintf(cfg.Stdout, "%s  %s\n", line, text)
	}
	return nil
}

func runAttach(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("attach", cfg.Stderr)
	var item, name, message string
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.StringVar(&name, "as", "", "attachment name (default: the file's name)")
	fs.StringVar(&message, "m", "", "note recorded with the attachment")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if fs.NArg() != 1 {
		return usageErr{errors.New("exactly one file is required")}
	}
	path := fs.Arg(0)
	if !filepath.IsAbs(path) && cfg.CWD != "" {
		path = filepath.Join(cfg.CWD, path)
	}
	if name == "" {
		name = filepath.Base(path)
	}
	if err := itemnotes.ValidName(name); err != nil {
		return usageErr{err}
	}
	if message != "" {
		if _, err := itemnotes.CleanText(message); err != nil {
			return usageErr{err}
		}
	}
	content, err := readAttachment(path)
	if err != nil {
		return err
	}
	m, err := cfg.App.ResolveItem(ctx, appResolve(cfg, item))
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	note, err := cfg.Coordinator.Attach(requestCtx, coordinator.AttachRequest{ItemID: m.ID, Name: name, Content: content, Text: message})
	cancel()
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, note)
	}
	fmt.Fprintf(cfg.Stdout, "attached %s to %s (%s)\n", name, m.ID, byteSize(note.Attachment.Size))
	return nil
}

// readAttachment reads a file to attach, refusing what is not a regular file
// or is too large before reading it.
func readAttachment(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > itemnotes.MaxAttachmentBytes {
		return nil, fmt.Errorf("%s is %s; attachments are at most %s", path, byteSize(info.Size()), byteSize(itemnotes.MaxAttachmentBytes))
	}
	content, err := io.ReadAll(io.LimitReader(file, itemnotes.MaxAttachmentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > itemnotes.MaxAttachmentBytes {
		return nil, fmt.Errorf("%s grew past %s while being read", path, byteSize(itemnotes.MaxAttachmentBytes))
	}
	return content, nil
}

func runAttachments(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	m, notes, err := readItemNotes(ctx, "attachments", args, cfg)
	if err != nil {
		return err
	}
	attachments := itemnotes.Attachments(notes)
	if jsonOut {
		return writeJSON(cfg.Stdout, struct {
			WorkItemID  string              `json:"work_item_id"`
			Attachments []itemnotes.Current `json:"attachments"`
		}{m.ID, attachments})
	}
	for _, attachment := range attachments {
		fmt.Fprintf(cfg.Stdout, "%s  %8s  %s\n", attachment.Time.Local().Format("2006-01-02 15:04"), byteSize(attachment.Size), attachment.Name)
	}
	return nil
}

func runAttachment(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{errors.New("attachment requires a subcommand: cat")}
	}
	switch args[0] {
	case "cat":
		return runAttachmentCat(ctx, args[1:], cfg)
	default:
		return usageErr{fmt.Errorf("unknown attachment subcommand %q", args[0])}
	}
}

var digestArgument = regexp.MustCompile(`^[0-9a-f]{64}$`)

func runAttachmentCat(ctx context.Context, args []string, cfg Config) error {
	fs := newFlagSet("attachment cat", cfg.Stderr)
	var item string
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if fs.NArg() != 1 {
		return usageErr{errors.New("exactly one attachment name or digest is required")}
	}
	m, err := cfg.App.ResolveItem(ctx, appResolve(cfg, item))
	if err != nil {
		return err
	}
	dir := cfg.App.Store.ItemDir(m.ID)
	notes, err := itemnotes.Read(dir)
	if err != nil {
		return err
	}
	// A digest names earlier content that an older note recorded.
	digest := fs.Arg(0)
	if current, ok := itemnotes.Find(notes, fs.Arg(0)); ok {
		digest = current.SHA256
	} else if !digestArgument.MatchString(digest) || !recorded(notes, digest) {
		return missingAttachment(fmt.Sprintf("no attachment %q on %s", fs.Arg(0), m.ID))
	}
	file, err := itemnotes.OpenBlob(dir, digest)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(cfg.Stdout, file)
	return err
}

// missingAttachment exits as not found without claiming the item is missing.
type missingAttachment string

func (e missingAttachment) Error() string        { return string(e) }
func (e missingAttachment) Is(target error) bool { return target == store.ErrNotFound }

func recorded(notes []itemnotes.Note, digest string) bool {
	for _, note := range notes {
		if note.Attachment != nil && note.Attachment.SHA256 == digest {
			return true
		}
	}
	return false
}

func readItemNotes(ctx context.Context, command string, args []string, cfg Config) (model.Manifest, []itemnotes.Note, error) {
	fs := newFlagSet(command, cfg.Stderr)
	var item string
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	if err := fs.Parse(args); err != nil {
		return model.Manifest{}, nil, usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return model.Manifest{}, nil, usageErr{err}
	}
	m, err := cfg.App.ResolveItem(ctx, appResolve(cfg, selector))
	if err != nil {
		return model.Manifest{}, nil, err
	}
	notes, err := itemnotes.Read(cfg.App.Store.ItemDir(m.ID))
	return m, notes, err
}

func shortSHA(sha string) string {
	if len(sha) > 9 {
		return sha[:9]
	}
	return sha
}

func byteSize(size int64) string {
	switch {
	case size >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(size)/(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(size)/(1<<10))
	default:
		return fmt.Sprintf("%d B", size)
	}
}
