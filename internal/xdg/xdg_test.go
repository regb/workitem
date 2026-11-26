package xdg_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/xdg"
)

// Each XDG variable moves its own root, falling back to the home directory;
// state and runtime directories are keyed by the data root.
func TestResolve(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	override := xdg.Map{}
	for _, name := range []string{"DATA", "CONFIG", "CACHE", "STATE"} {
		override["XDG_"+name+"_HOME"] = filepath.Join(base, name)
	}
	override["XDG_RUNTIME_DIR"] = filepath.Join(base, "RUNTIME")
	keys := map[string]bool{}
	for _, tc := range []struct {
		env                                 xdg.Map
		data, config, cache, state, runtime string
	}{
		{xdg.Map{}, filepath.Join(home, ".local", "share"), filepath.Join(home, ".config"), filepath.Join(home, ".cache"), filepath.Join(home, ".local", "state"), filepath.Join(os.TempDir(), fmt.Sprintf("wi-%d", os.Getuid()))},
		{override, filepath.Join(base, "DATA"), filepath.Join(base, "CONFIG"), filepath.Join(base, "CACHE"), filepath.Join(base, "STATE"), filepath.Join(base, "RUNTIME")},
	} {
		paths, err := xdg.Resolve(tc.env, home)
		if err != nil {
			t.Fatal(err)
		}
		got := []string{paths.DataRoot, paths.ItemsDir, paths.ConfigFile, paths.CacheRoot, paths.DataStateRoot, paths.DataRuntimeRoot}
		want := []string{
			filepath.Join(tc.data, "wi"), filepath.Join(tc.data, "wi", "items"), filepath.Join(tc.config, "wi", "config.toml"), filepath.Join(tc.cache, "wi"),
			filepath.Join(tc.state, "wi", paths.DataRootKey), filepath.Join(tc.runtime, "wi", paths.DataRootKey),
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("env %v:\n got %q\nwant %q", tc.env, got, want)
		}
		if paths.DataRootKey == "" || keys[paths.DataRootKey] {
			t.Errorf("data root key %q is empty or shared", paths.DataRootKey)
		}
		keys[paths.DataRootKey] = true
	}
}
