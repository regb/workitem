package coordinator

import (
	"context"
	"fmt"
	"strings"
	"time"

	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/itemnotes"
	"github.com/regb/workitem/internal/model"
)

const (
	MethodAddNote = "add_note"
	MethodAttach  = "attach"
)

// AddNoteRequest appends a note to an item's log.
type AddNoteRequest struct {
	ItemID string `json:"item_id"`
	Text   string `json:"text"`
}

// AttachRequest stores content under a name in an item and records it in
// the item's log, with Text or a default line.
type AttachRequest struct {
	ItemID  string `json:"item_id"`
	Name    string `json:"name"`
	Content []byte `json:"content"`
	Text    string `json:"text,omitempty"`
}

func (c *Client) AddNote(ctx context.Context, request AddNoteRequest) (itemnotes.Note, error) {
	var result itemnotes.Note
	err := c.Call(ctx, MethodAddNote, request, &result)
	return result, err
}

func (c *Client) Attach(ctx context.Context, request AttachRequest) (itemnotes.Note, error) {
	var result itemnotes.Note
	err := c.Call(ctx, MethodAttach, request, &result)
	return result, err
}

func (s *Server) addNote(ctx context.Context, request AddNoteRequest) (itemnotes.Note, error) {
	text, err := itemnotes.CleanText(request.Text)
	if err != nil {
		return itemnotes.Note{}, err
	}
	return s.appendNote(ctx, request.ItemID, itemnotes.Note{Text: text}, nil)
}

func (s *Server) attach(ctx context.Context, request AttachRequest) (itemnotes.Note, error) {
	if err := itemnotes.ValidName(request.Name); err != nil {
		return itemnotes.Note{}, err
	}
	if len(request.Content) > itemnotes.MaxAttachmentBytes {
		return itemnotes.Note{}, fmt.Errorf("attachment is %d bytes; the limit is %d", len(request.Content), itemnotes.MaxAttachmentBytes)
	}
	text := "Attached " + request.Name
	if strings.TrimSpace(request.Text) != "" {
		var err error
		if text, err = itemnotes.CleanText(request.Text); err != nil {
			return itemnotes.Note{}, err
		}
	}
	note := itemnotes.Note{Text: text, Attachment: &itemnotes.Attachment{Name: request.Name, Size: int64(len(request.Content)), Type: itemnotes.ContentType(request.Name, request.Content)}}
	return s.appendNote(ctx, request.ItemID, note, request.Content)
}

// appendNote records a note, after storing its attachment's content. The
// content is stored first, so a note never names content that is missing.
func (s *Server) appendNote(ctx context.Context, itemID string, note itemnotes.Note, content []byte) (itemnotes.Note, error) {
	if !model.ValidID(itemID) {
		return itemnotes.Note{}, fmt.Errorf("invalid work item id %q", itemID)
	}
	item, err := s.database.CanonicalManifest(itemID)
	if err != nil {
		return itemnotes.Note{}, err
	}
	note.Head = worktreeHead(ctx, item.Manifest)
	s.domainMu.Lock()
	defer s.domainMu.Unlock()
	// The item may have been deleted while its worktree was read.
	if _, err := s.database.CanonicalManifest(itemID); err != nil {
		return itemnotes.Note{}, err
	}
	dir := s.native.ItemDir(itemID)
	if note.Attachment != nil {
		if note.Attachment.SHA256, err = itemnotes.WriteBlob(dir, content); err != nil {
			return itemnotes.Note{}, fmt.Errorf("store attachment: %w", err)
		}
	}
	note.Time = time.Now().UTC()
	if err := itemnotes.Append(dir, note); err != nil {
		return itemnotes.Note{}, fmt.Errorf("record note: %w", err)
	}
	s.database.changes.record(itemID)
	return note, nil
}

// worktreeHead is the commit checked out in the item's worktree, if it has
// one that Git can read.
func worktreeHead(ctx context.Context, manifest model.Manifest) string {
	if manifest.Checkout.Claim == nil || strings.TrimSpace(manifest.Checkout.Claim.Path) == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	head, err := gitpkg.New("git").RevParse(ctx, manifest.Checkout.Claim.Path, "HEAD")
	if err != nil {
		return ""
	}
	return head
}
