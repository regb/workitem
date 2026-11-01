package coordinator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/model"
)

func TestAgentProfilesKeepADefault(t *testing.T) {
	db, err := OpenDatabase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	executable := filepath.Join(t.TempDir(), "pi")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	empty, err := db.AgentProfiles()
	if err != nil || len(empty.Profiles) != 0 || empty.Default != "" {
		t.Fatal(empty, err)
	}
	pi := model.AgentProfile{Name: "pi", Driver: "pi", Label: "Pi", Config: map[string]any{"executable": executable}}
	first, err := db.ChangeAgentProfile(ChangeAgentProfileCommand{Put: &pi})
	if err != nil || first.Default != "pi" || first.Revision != 1 {
		t.Fatal("the first profile becomes the default", first, err)
	}
	claude := model.AgentProfile{Name: "claude", Driver: "claude"}
	if _, err := db.ChangeAgentProfile(ChangeAgentProfileCommand{Put: &claude}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ChangeAgentProfile(ChangeAgentProfileCommand{Remove: "pi"}); err == nil {
		t.Fatal("removed the default while another profile exists")
	}
	stale := uint64(1)
	if _, err := db.ChangeAgentProfile(ChangeAgentProfileCommand{ExpectedRevision: &stale, Default: "claude"}); err == nil {
		t.Fatal("stale change accepted")
	}
	if switched, err := db.ChangeAgentProfile(ChangeAgentProfileCommand{Default: "claude"}); err != nil || switched.Default != "claude" {
		t.Fatal(switched, err)
	}
	removed, err := db.ChangeAgentProfile(ChangeAgentProfileCommand{Remove: "pi"})
	if err != nil || len(removed.Profiles) != 1 {
		t.Fatal(removed, err)
	}
	for _, bad := range []model.AgentProfile{
		{Name: "Default", Driver: "pi"}, {Name: "default", Driver: "pi"}, {Name: "x"},
		{Name: "rel", Driver: "pi", Config: map[string]any{"executable": "bin/pi"}},
		{Name: "missing", Driver: "pi", Config: map[string]any{"executable": "/nonexistent/pi"}},
		{Name: "dir", Driver: "pi", Config: map[string]any{"executable": t.TempDir()}},
	} {
		if _, err := db.ChangeAgentProfile(ChangeAgentProfileCommand{Put: &bad}); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
