package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/tmux"
)

func AttachTerminal(ctx context.Context, masterID, itemID string) error {
	if err := validateMasterID(masterID); err != nil {
		return err
	}
	if !model.ValidID(itemID) {
		return fmt.Errorf("invalid work item id %q", itemID)
	}
	root, err := workerRoot(masterID)
	if err != nil {
		return err
	}
	paths, err := workerPaths(root, masterID)
	if err != nil {
		return err
	}
	socket, err := coordinator.SocketPath(paths.RuntimeDir, paths.DataRoot)
	if err != nil {
		return err
	}
	canonical, err := (&coordinator.Client{SocketPath: socket}).CanonicalManifest(ctx, itemID)
	if err != nil {
		return err
	}
	manifest := canonical.Manifest
	runtimeDir := filepath.Join(os.TempDir(), fmt.Sprintf("wi-executor-%d", os.Getuid()), shortKey(masterID))
	configPath := filepath.Join(mustUserCacheDir(), "wi", "executors", shortKey(masterID), "tmux.conf")
	client := tmux.NewWithServer("tmux", filepath.Join(runtimeDir, "tmux.sock"), configPath)
	if content, err := os.ReadFile(filepath.Join(root, "authority.json")); err == nil {
		var authority workerAuthorityContext
		if json.Unmarshal(content, &authority) == nil && validExecutorName(authority.Executor) {
			if err := client.SetSessionEnvironment(ctx, manifest.TerminalSessionName(), map[string]string{"WI_EXECUTOR_DAEMON_SOCKET": workerAuthoritySocket(root), "WI_EXECUTOR": authority.Executor}); err != nil {
				return err
			}
		}
	}
	return client.AttachOrSwitch(ctx, manifest.TerminalSessionName(), false)
}
