package plugin

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPluginInvocationPreservesRestrictedEndpoint(t *testing.T) {
	t.Setenv("WI_AGENT_DAEMON_SOCKET", "/test/restricted.sock")
	t.Setenv("WI_ID", "must-not-inherit")
	script := filepath.Join(t.TempDir(), "plugin")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n[ \"$WI_AGENT_DAEMON_SOCKET\" = /test/restricted.sock ] && [ -z \"$WI_ID\" ] || exit 11\n[ \"$1\" = skill ] && echo 'Safe instructions'\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Name: "demo", Command: []string{script}, Instructions: "Safe instructions"}
	var out bytes.Buffer
	if err := RunOnce(context.Background(), spec, "wi", &out, &out); err != nil {
		t.Fatal("run lost authority boundary", err)
	}
	if text, err := Skill(context.Background(), spec, "wi"); err != nil || text != "Safe instructions" {
		t.Fatal("skill lost authority boundary", err)
	}
}
func TestStaticInstructionsAreBounded(t *testing.T) {
	if text, err := Skill(context.Background(), Spec{Name: "verbose", Instructions: strings.Repeat("x", MaxSkillBytes+1)}, "wi"); err == nil || text != "" {
		t.Fatal("oversized instructions were accepted")
	}
}
func TestPluginCancellationBoundsInheritedOutputPipes(t *testing.T) {
	script := filepath.Join(t.TempDir(), "plugin")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 20 &\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	var out bytes.Buffer
	err := RunOnce(ctx, Spec{Name: "cancel", Command: []string{script}}, "wi", &out, &out)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 3*time.Second {
		t.Fatal("plugin cancellation blocked", err)
	}
}
