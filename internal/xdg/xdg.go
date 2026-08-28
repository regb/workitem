package xdg

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/regb/workitem/internal/dataroot"
)

// Paths contains wi's XDG-resolved filesystem locations.
type Paths struct {
	Home       string
	DataHome   string
	ConfigHome string
	CacheHome  string
	StateHome  string
	RuntimeDir string

	DataRoot        string
	DataRootKey     string
	ConfigRoot      string
	ConfigFile      string
	CacheRoot       string
	StateRoot       string
	DataStateRoot   string
	DataRuntimeRoot string
	ItemsDir        string
	WorktreesDir    string
}

// FromEnv resolves XDG paths using the process environment.
func FromEnv() (Paths, error) {
	return Resolve(getenvMap{}, "")
}

// Resolve resolves wi paths from env and home. If home is empty, os.UserHomeDir is used.
func Resolve(env Env, home string) (Paths, error) {
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return Paths{}, fmt.Errorf("resolve home directory: %w", err)
		}
	}
	if home == "" {
		return Paths{}, fmt.Errorf("home directory is empty")
	}

	dataHome := env.Get("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	configHome := env.Get("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	cacheHome := env.Get("XDG_CACHE_HOME")
	if cacheHome == "" {
		cacheHome = filepath.Join(home, ".cache")
	}
	stateHome := env.Get("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(home, ".local", "state")
	}
	runtimeDir := env.Get("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = filepath.Join(os.TempDir(), fmt.Sprintf("wi-%d", os.Getuid()))
	}

	dataRoot := filepath.Join(dataHome, "wi")
	dataRootKey, err := dataroot.Key(dataRoot)
	if err != nil {
		return Paths{}, err
	}
	configRoot := filepath.Join(configHome, "wi")
	cacheRoot := filepath.Join(cacheHome, "wi")
	stateRoot := filepath.Join(stateHome, "wi")

	return Paths{
		Home:            home,
		DataHome:        dataHome,
		ConfigHome:      configHome,
		CacheHome:       cacheHome,
		StateHome:       stateHome,
		RuntimeDir:      runtimeDir,
		DataRoot:        dataRoot,
		DataRootKey:     dataRootKey,
		ConfigRoot:      configRoot,
		ConfigFile:      filepath.Join(configRoot, "config.toml"),
		CacheRoot:       cacheRoot,
		StateRoot:       stateRoot,
		DataStateRoot:   filepath.Join(stateRoot, dataRootKey),
		DataRuntimeRoot: filepath.Join(runtimeDir, "wi", dataRootKey),
		ItemsDir:        filepath.Join(dataRoot, "items"),
		WorktreesDir:    filepath.Join(dataRoot, "worktrees"),
	}, nil
}

// Env abstracts environment lookup for tests.
type Env interface {
	Get(key string) string
}

type getenvMap struct{}

func (getenvMap) Get(key string) string { return os.Getenv(key) }

// Map is a simple Env implementation for tests and CLI dependency wiring.
type Map map[string]string

func (m Map) Get(key string) string { return m[key] }
