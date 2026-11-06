package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const testProcessRoot = "WI_TEST_PROCESS_ROOT"

// IsolatedEnvironment keeps tooling available but removes live Wi, Pi, tmux,
// and direnv context. All filesystem namespaces belong to the supplied root.
func IsolatedEnvironment(base []string, root string) []string {
	env := make([]string, 0, len(base)+8)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if key == "HOME" || strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "WI_") || strings.HasPrefix(key, "PI_") || strings.HasPrefix(key, "DIRENV_") || strings.HasPrefix(key, "GIT_") || key == "GOBIN" || key == "GOPATH" || key == "TMUX" || key == "TMUX_PANE" || key == "TMUX_TMPDIR" {
			continue
		}
		env = append(env, entry)
	}
	for _, pair := range [][2]string{
		{"HOME", "home"}, {"XDG_DATA_HOME", "data"}, {"XDG_CONFIG_HOME", "config"},
		{"XDG_CACHE_HOME", "cache"}, {"XDG_STATE_HOME", "state"}, {"XDG_RUNTIME_DIR", "run"},
		{"PI_CODING_AGENT_DIR", "pi"}, {"GOBIN", "bin"}, {"GOPATH", "go"}, {"TMUX_TMPDIR", "tmux"},
	} {
		env = append(env, pair[0]+"="+filepath.Join(root, pair[1]))
	}
	return append(env, testProcessRoot+"="+root, "GIT_CONFIG_NOSYSTEM=1")
}

// RunIsolatedTests is a TestMain guard against accidental use of an operator's
// data root or sockets. Explicit helper-process children reuse the parent's
// namespace so their test bootstrap and runtime variables remain intact.
func RunIsolatedTests(run func() int) int {
	if root := os.Getenv(testProcessRoot); filepath.IsAbs(root) && strings.HasPrefix(filepath.Base(root), "wi-tests-") {
		if info, err := os.Lstat(root); err == nil && info.IsDir() && info.Mode().Perm() == 0700 {
			return run()
		}
	}
	root, err := os.MkdirTemp("/tmp", "wi-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "isolate tests:", err)
		return 1
	}
	defer os.RemoveAll(root)
	for _, part := range []string{"home", "data", "config", "cache", "state", "run", "pi", "tmux"} {
		if err := os.Mkdir(filepath.Join(root, part), 0700); err != nil {
			fmt.Fprintln(os.Stderr, "isolate tests:", err)
			return 1
		}
	}
	// Preserve only the shared Go compiler/module caches, not installation
	// destinations. This keeps real-binary tests offline after changing HOME.
	if output, err := exec.Command("go", "env", "GOCACHE", "GOMODCACHE").Output(); err == nil {
		values := strings.Split(strings.TrimSpace(string(output)), "\n")
		if len(values) == 2 {
			for i, key := range []string{"GOCACHE", "GOMODCACHE"} {
				if os.Getenv(key) == "" && filepath.IsAbs(values[i]) {
					_ = os.Setenv(key, values[i])
				}
			}
		}
	}
	// os.Environ users and implicitly inherited subprocess environments must
	// see the same isolation as commands with explicit test environments.
	env := IsolatedEnvironment(os.Environ(), root)
	os.Clearenv()
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if err := os.Setenv(key, value); err != nil {
			fmt.Fprintln(os.Stderr, "isolate tests:", err)
			return 1
		}
	}
	return run()
}
