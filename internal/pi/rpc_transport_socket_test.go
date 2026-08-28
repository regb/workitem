package pi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeRPCRequiresDaemonSocket(t *testing.T) {
	dir := t.TempDir()
	fakePi := filepath.Join(dir, "fake-pi")
	if err := os.WriteFile(fakePi, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := runNativeRPC(context.Background(), fakePi, nil, os.Environ(), ExecSpec{
		CWD: dir, WorkItemID: "01K00000000000000000000000", RuntimeID: "rpc-socket", ControlSocketPath: filepath.Join(dir, "control.sock"),
	})
	if err == nil || !strings.Contains(err.Error(), "requires a daemon socket path") {
		t.Fatalf("err = %v", err)
	}
}
