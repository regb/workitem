package pi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	protocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/skills"
)

func TestLaunchCleansTemporarySkillsOnFailure(t *testing.T) {
	for _, rejectReady := range []bool{false, true} {
		root := t.TempDir()
		t.Setenv("TMPDIR", root)
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
		backend := filepath.Join(t.TempDir(), "failing-pi")
		if err := os.WriteFile(backend, []byte("#!/bin/sh\nexit 42\n"), 0700); err != nil {
			t.Fatal(err)
		}
		request := protocol.LaunchRequest{AppendSystemPrompt: "host instructions", Skills: skills.Bundled(), Config: []byte(`{"executable":"` + backend + `"}`), Runtime: protocol.Runtime{Mode: protocol.ModeTUI, CWD: t.TempDir()}, Conversation: protocol.Conversation{Locator: "/tmp/test-session.jsonl"}}
		replyError := errors.New("readiness rejected")
		err := launch(context.Background(), request, func(protocol.Ready) error {
			if rejectReady {
				return replyError
			}
			return nil
		})
		if err == nil || rejectReady && !errors.Is(err, replyError) {
			t.Fatalf("rejectReady=%v error=%v", rejectReady, err)
		}
		files, err := os.ReadDir(root)
		if err != nil || len(files) != 0 {
			t.Fatalf("temporary files survived failure: %v error=%v", files, err)
		}
	}
}

func TestLaunchRejectsUnsafeSkillBeforeBackendStartup(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	backend := filepath.Join(t.TempDir(), "pi")
	if err := os.WriteFile(backend, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	request := protocol.LaunchRequest{Skills: []protocol.Skill{{Name: "wi", Files: []protocol.SkillFile{{Path: "../outside"}}}}, Config: []byte(`{"executable":"` + backend + `"}`), Runtime: protocol.Runtime{Mode: protocol.ModeTUI}}
	var response protocol.Ready
	if err := launch(context.Background(), request, func(r protocol.Ready) error { response = r; return nil }); err == nil || !strings.Contains(response.Error, "skill path") {
		t.Fatalf("error=%v response=%+v", err, response)
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 0 {
		t.Fatalf("invalid payload created files: %v error=%v", files, err)
	}
}
