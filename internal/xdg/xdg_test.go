package xdg_test

import (
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/xdg"
)

func TestResolveDefaults(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	paths, err := xdg.Resolve(xdg.Map{}, home)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := filepath.Join(home, ".local", "share", "wi")
	if paths.DataRoot != wantRoot {
		t.Fatalf("DataRoot = %q, want %q", paths.DataRoot, wantRoot)
	}
	if paths.ItemsDir != filepath.Join(wantRoot, "items") {
		t.Fatalf("ItemsDir = %q", paths.ItemsDir)
	}
	if paths.ConfigFile != filepath.Join(home, ".config", "wi", "config.toml") {
		t.Fatalf("ConfigFile = %q", paths.ConfigFile)
	}
	if paths.CacheRoot != filepath.Join(home, ".cache", "wi") {
		t.Fatalf("CacheRoot = %q", paths.CacheRoot)
	}
	if paths.StateRoot != filepath.Join(home, ".local", "state", "wi") || paths.DataStateRoot != filepath.Join(paths.StateRoot, paths.DataRootKey) {
		t.Fatalf("state paths = root %q data %q", paths.StateRoot, paths.DataStateRoot)
	}
	if paths.DataRuntimeRoot != filepath.Join(paths.RuntimeDir, "wi", paths.DataRootKey) {
		t.Fatalf("DataRuntimeRoot = %q", paths.DataRuntimeRoot)
	}
}

func TestResolveXDGOverrides(t *testing.T) {
	base := t.TempDir()
	paths, err := xdg.Resolve(xdg.Map{
		"XDG_DATA_HOME":   filepath.Join(base, "data"),
		"XDG_CONFIG_HOME": filepath.Join(base, "config"),
		"XDG_CACHE_HOME":  filepath.Join(base, "cache"),
		"XDG_STATE_HOME":  filepath.Join(base, "state"),
		"XDG_RUNTIME_DIR": filepath.Join(base, "runtime"),
	}, filepath.Join(base, "home"))
	if err != nil {
		t.Fatal(err)
	}
	if paths.DataRoot != filepath.Join(base, "data", "wi") {
		t.Fatalf("DataRoot = %q", paths.DataRoot)
	}
	if paths.ConfigFile != filepath.Join(base, "config", "wi", "config.toml") {
		t.Fatalf("ConfigFile = %q", paths.ConfigFile)
	}
	if paths.CacheRoot != filepath.Join(base, "cache", "wi") {
		t.Fatalf("CacheRoot = %q", paths.CacheRoot)
	}
	if paths.DataStateRoot != filepath.Join(base, "state", "wi", paths.DataRootKey) {
		t.Fatalf("DataStateRoot = %q", paths.DataStateRoot)
	}
	if paths.DataRuntimeRoot != filepath.Join(base, "runtime", "wi", paths.DataRootKey) {
		t.Fatalf("DataRuntimeRoot = %q", paths.DataRuntimeRoot)
	}
}
