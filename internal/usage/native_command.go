package usage

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

type boundedOutput struct{ data []byte }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > 1<<20 {
		return 0, fmt.Errorf("native output exceeded limit")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

// RunNativeCommand isolates the working directory and bounds output/lifetime.
// Authentication remains inside the native executable. Raw errors and stderr
// are deliberately not returned because native diagnostics may contain secrets.
func RunNativeCommand(ctx context.Context, command, args []string, environment map[string]string) ([]byte, error) {
	return RunNativeCommandWithFiles(ctx, command, args, environment, nil)
}

// RunNativeCommandWithFiles places bundled, non-secret helper files in the
// private working directory. Caller configuration and environment are unchanged.
func RunNativeCommandWithFiles(ctx context.Context, command, args []string, environment map[string]string, files map[string][]byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if len(command) == 0 {
		return nil, fmt.Errorf("native executable unavailable")
	}
	dir, err := os.MkdirTemp("", "wi-native-usage-")
	if err != nil {
		return nil, fmt.Errorf("create native usage working directory")
	}
	defer os.RemoveAll(dir)
	for name, data := range files {
		if !filepath.IsLocal(name) {
			return nil, fmt.Errorf("invalid native helper path")
		}
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, fmt.Errorf("create native helper directory")
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			return nil, fmt.Errorf("write native helper")
		}
	}
	argv := append(append([]string{}, command[1:]...), args...)
	cmd := exec.CommandContext(ctx, command[0], argv...)
	cmd.Dir = dir
	keys := []string{}
	for k := range environment {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+environment[k])
	}
	output := &boundedOutput{}
	cmd.Stdout = output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("native usage command failed or timed out")
	}
	return output.data, nil
}
