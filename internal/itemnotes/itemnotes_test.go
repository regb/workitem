package itemnotes

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNotesAppendInOrderAndSurviveAnInterruptedAppend(t *testing.T) {
	dir := t.TempDir()
	if notes, err := Read(dir); err != nil || len(notes) != 0 {
		t.Fatalf("missing log: %v %v", notes, err)
	}
	first := Note{Time: time.Unix(1, 0).UTC(), Text: "first"}
	if err := Append(dir, first); err != nil {
		t.Fatal(err)
	}
	// An append cut short leaves half a line.
	file, err := os.OpenFile(filepath.Join(dir, NotesFilename), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString(`{"time":"2026`)
	file.Close()
	notes, err := Read(dir)
	if err != nil || len(notes) != 1 {
		t.Fatalf("torn tail: %v %v", notes, err)
	}
	if err := Append(dir, Note{Time: time.Unix(2, 0).UTC(), Text: "second"}); err != nil {
		t.Fatal(err)
	}
	notes, err = Read(dir)
	if err != nil || len(notes) != 2 || notes[1].Text != "second" {
		t.Fatalf("the next append replaces the torn line: %+v %v", notes, err)
	}
}

func TestAttachmentsKeepEarlierContentByDigest(t *testing.T) {
	dir := t.TempDir()
	var notes []Note
	for i, content := range []string{"one", "two"} {
		digest, err := WriteBlob(dir, []byte(content))
		if err != nil {
			t.Fatal(err)
		}
		notes = append(notes, Note{Time: time.Unix(int64(i), 0), Text: "Attached REPORT.md", Attachment: &Attachment{Name: "REPORT.md", SHA256: digest, Size: 3}})
	}
	current := Attachments(notes)
	if len(current) != 1 || current[0].SHA256 != notes[1].Attachment.SHA256 {
		t.Fatalf("current: %+v", current)
	}
	file, err := OpenBlob(dir, notes[0].Attachment.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if content, _ := io.ReadAll(file); string(content) != "one" {
		t.Fatalf("earlier content: %q", content)
	}
	if _, err := OpenBlob(dir, "../notes.jsonl"); err == nil {
		t.Fatal("a digest must not name another file")
	}
	if again, err := WriteBlob(dir, []byte("one")); err != nil || again != notes[0].Attachment.SHA256 {
		t.Fatalf("same content: %s %v", again, err)
	}
}

func TestNotesAndNamesAreChecked(t *testing.T) {
	for _, text := range []string{"", "  ", strings.Repeat("x", MaxNoteBytes+1), "bell\a"} {
		if _, err := CleanText(text); err == nil {
			t.Errorf("accepted note %q", text)
		}
	}
	if text, err := CleanText("  line one\r\nline two\t \n"); err != nil || text != "line one\nline two" {
		t.Errorf("clean: %q %v", text, err)
	}
	for _, name := range []string{"", ".env", "a/b", `a\b`, " x", "x\n", strings.Repeat("a", 129)} {
		if ValidName(name) == nil {
			t.Errorf("accepted name %q", name)
		}
	}
	for _, name := range []string{"REPORT.md", "login bug.png", "résumé.pdf"} {
		if err := ValidName(name); err != nil {
			t.Errorf("refused name %q: %v", name, err)
		}
	}
}

func TestLinksAreNotFollowed(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("secret\n"), 0o600)
	if err := os.Symlink(outside, filepath.Join(dir, NotesFilename)); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err == nil {
		t.Fatal("read a linked log")
	}
	if err := Append(dir, Note{Text: "x"}); err == nil {
		t.Fatal("appended through a link")
	}
}
