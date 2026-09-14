package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	executorprotocol "github.com/regb/workitem/executor/protocol"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/tmux"
	"github.com/regb/workitem/internal/xdg"
)

type terminalAttachment struct {
	root     string
	paths    xdg.Paths
	manifest model.Manifest
	tmux     tmux.Client
}

func AttachAgentTerminal(ctx context.Context, masterID, itemID string) error {
	attachment, err := loadTerminalAttachment(ctx, masterID, itemID)
	if err != nil {
		return err
	}
	return attachAgentTerminal(ctx, attachment)
}

func AttachShellTerminal(ctx context.Context, masterID, itemID string) error {
	attachment, err := loadTerminalAttachment(ctx, masterID, itemID)
	if err != nil {
		return err
	}
	return attachShellTerminal(ctx, masterID, attachment)
}

func attachAgentTerminal(ctx context.Context, attachment terminalAttachment) error {
	environment := workerAuthorityEnvironment(attachment.root)
	if len(environment) > 0 {
		if err := attachment.tmux.SetSessionEnvironment(ctx, attachment.manifest.TerminalSessionName(), environment); err != nil {
			return err
		}
	}
	return attachment.tmux.AttachOrSwitch(ctx, attachment.manifest.TerminalSessionName(), false)
}

func attachShellTerminal(ctx context.Context, masterID string, attachment terminalAttachment) error {
	manifest := attachment.manifest
	if manifest.Checkout.Path == nil || *manifest.Checkout.Path == "" {
		return fmt.Errorf("work item %s has no executor workspace", manifest.ID)
	}
	environment, err := attachment.tmux.SessionEnvironment(ctx, manifest.TerminalSessionName())
	if err != nil {
		return fmt.Errorf("read executor terminal environment: %w", err)
	}
	for key := range environment {
		if key == "TMUX" || key == "TMUX_PANE" || key == "WI_TMUX_SOCKET" || key == "WI_TMUX_CONFIG" || strings.HasPrefix(key, "WI_AGENT_") {
			delete(environment, key)
		}
	}
	environment["WI_ID"] = manifest.ID
	environment["WI_DIR"] = filepath.Join(attachment.paths.ItemsDir, manifest.ID)
	environment["WI_WORKTREE"] = *manifest.Checkout.Path
	delete(environment, "WI_REPOSITORY")
	for key, value := range workerAuthorityEnvironment(attachment.root) {
		environment[key] = value
	}
	profile, err := loadTerminalProfile(masterID)
	if err != nil {
		return err
	}
	shell := strings.TrimSpace(profile.Shell)
	if shell == "" {
		shell = strings.TrimSpace(os.Getenv("SHELL"))
	}
	if shell == "" {
		shell = "/bin/sh"
	}
	id, err := model.NewID()
	if err != nil {
		return err
	}
	sessionName := shellSessionPrefix(manifest.ID) + strings.ToLower(id[len(id)-8:])
	if _, err := attachment.tmux.EnsureCommandSession(ctx, tmux.SessionSpec{Name: sessionName, CWD: *manifest.Checkout.Path, Env: environment}, "shell", []string{shell, "-l"}); err != nil {
		return err
	}
	return attachment.tmux.AttachOrSwitch(ctx, sessionName, false)
}

func loadTerminalAttachment(ctx context.Context, masterID, itemID string) (terminalAttachment, error) {
	if err := validateMasterID(masterID); err != nil {
		return terminalAttachment{}, err
	}
	if !model.ValidID(itemID) {
		return terminalAttachment{}, fmt.Errorf("invalid work item id %q", itemID)
	}
	root, err := workerRoot(masterID)
	if err != nil {
		return terminalAttachment{}, err
	}
	paths, err := workerPaths(root, masterID)
	if err != nil {
		return terminalAttachment{}, err
	}
	socket, err := coordinator.SocketPath(paths.RuntimeDir, paths.DataRoot)
	if err != nil {
		return terminalAttachment{}, err
	}
	canonical, err := (&coordinator.Client{SocketPath: socket}).CanonicalManifest(ctx, itemID)
	if err != nil {
		return terminalAttachment{}, err
	}
	return terminalAttachment{root: root, paths: paths, manifest: canonical.Manifest, tmux: executorTmuxClient(masterID)}, nil
}

func workerAuthorityEnvironment(root string) map[string]string {
	content, err := os.ReadFile(filepath.Join(root, "authority.json"))
	if err != nil {
		return nil
	}
	var authority workerAuthorityContext
	if json.Unmarshal(content, &authority) != nil || !validExecutorName(authority.Executor) {
		return nil
	}
	return map[string]string{"WI_EXECUTOR_DAEMON_SOCKET": workerAuthoritySocket(root), "WI_EXECUTOR": authority.Executor}
}

func loadTerminalProfile(masterID string) (executorprotocol.TerminalProfile, error) {
	path := filepath.Join(mustUserCacheDir(), "wi", "executors", shortKey(masterID), "terminal-profile.json")
	content, err := os.ReadFile(path)
	if err != nil {
		return executorprotocol.TerminalProfile{}, fmt.Errorf("read executor terminal profile: %w", err)
	}
	var profile executorprotocol.TerminalProfile
	if err := json.Unmarshal(content, &profile); err != nil {
		return executorprotocol.TerminalProfile{}, fmt.Errorf("decode executor terminal profile: %w", err)
	}
	return profile, nil
}

func executorTmuxClient(masterID string) tmux.Client {
	runtimeDir := filepath.Join(os.TempDir(), fmt.Sprintf("wi-executor-%d", os.Getuid()), shortKey(masterID))
	configPath := filepath.Join(mustUserCacheDir(), "wi", "executors", shortKey(masterID), "tmux.conf")
	return tmux.NewWithServer("tmux", filepath.Join(runtimeDir, "tmux.sock"), configPath)
}

func shellSessionPrefix(itemID string) string {
	short := itemID
	if len(short) > 12 {
		short = short[:12]
	}
	return "wi-shell-" + strings.ToLower(short) + "-"
}
