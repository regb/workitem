package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	executorprotocol "github.com/regb/workitem/executor/protocol"
	agentdriver "github.com/regb/workitem/internal/agentdriver"
	"github.com/regb/workitem/internal/coordinator"
	itemlock "github.com/regb/workitem/internal/lock"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/tmux"
	"github.com/regb/workitem/internal/version"
	"github.com/regb/workitem/internal/xdg"
)

type Worker struct {
	TmuxCommand string
}

func (w Worker) Handle(ctx context.Context, request executorprotocol.Request) (response executorprotocol.Response, err error) {
	if err := validateMasterID(request.MasterID); err != nil {
		return executorprotocol.Response{}, err
	}
	if itemID := requestItemID(request); itemID != "" {
		if !model.ValidID(itemID) {
			return executorprotocol.Response{}, fmt.Errorf("invalid executor work item id %q", itemID)
		}
		root, rootErr := workerRoot(request.MasterID)
		if rootErr != nil {
			return executorprotocol.Response{}, rootErr
		}
		locked, lockErr := itemlock.Acquire(ctx, filepath.Join(root, "locks", itemID+".lock"))
		if lockErr != nil {
			return executorprotocol.Response{}, lockErr
		}
		defer func() {
			if releaseErr := locked.Release(); err == nil && releaseErr != nil {
				err = releaseErr
			}
		}()
	}
	switch request.Operation {
	case executorprotocol.OperationDoctor:
		return w.doctor(ctx, request)
	case executorprotocol.OperationAuthorityInfo:
		return w.authorityInfo(request)
	case executorprotocol.OperationWorkspaceEnsure:
		return w.ensureWorkspace(ctx, request)
	case executorprotocol.OperationWorkspaceStatus:
		return w.workspaceStatus(ctx, request)
	case executorprotocol.OperationWorkspaceRelease:
		return w.workspaceRelease(ctx, request)
	case executorprotocol.OperationRuntimeEnsure:
		return w.runtimeEnsure(ctx, request)
	case executorprotocol.OperationRuntimeStatus:
		return w.runtimeStatus(ctx, request)
	case executorprotocol.OperationRuntimeControl:
		return w.runtimeControl(ctx, request)
	case executorprotocol.OperationTerminalClose:
		return w.terminalClose(ctx, request)
	case executorprotocol.OperationShutdown:
		return w.shutdown(ctx, request)
	default:
		return executorprotocol.Response{}, fmt.Errorf("unsupported executor operation %q", request.Operation)
	}
}

func requestItemID(request executorprotocol.Request) string {
	if request.Workspace != nil {
		return request.Workspace.ItemID
	}
	if request.Runtime != nil {
		return request.Runtime.ItemID
	}
	return ""
}

func (w Worker) authorityInfo(request executorprotocol.Request) (executorprotocol.Response, error) {
	if !validExecutorName(request.ExecutorName) {
		return executorprotocol.Response{}, fmt.Errorf("executor name is invalid")
	}
	root, err := workerRoot(request.MasterID)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return executorprotocol.Response{}, err
	}
	if err := writeWorkerAuthorityContext(root, request.ExecutorName); err != nil {
		return executorprotocol.Response{}, err
	}
	return executorprotocol.Response{Authority: &executorprotocol.AuthorityResult{SocketPath: workerAuthoritySocket(root)}}, nil
}

func (w Worker) doctor(ctx context.Context, request executorprotocol.Request) (executorprotocol.Response, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return executorprotocol.Response{}, fmt.Errorf("resolve worker home: %w", err)
	}
	hostname, _ := os.Hostname()
	result := executorprotocol.DoctorResult{
		WorkerID:      hostname,
		Version:       version.Current().Version,
		BuildIdentity: version.BuildIdentity(),
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		Home:          home,
		Tools:         map[string]string{},
	}
	for _, tool := range []string{"git", "tmux", "pi", "bash"} {
		result.Tools[tool] = toolVersion(ctx, tool)
	}
	profile := executorprotocol.TerminalProfile{}
	if request.Terminal != nil {
		profile = *request.Terminal
	}
	probe, err := w.probeTmux(ctx, request.MasterID, home, profile)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	result.Tmux = probe
	return executorprotocol.Response{Doctor: &result}, nil
}

func (w Worker) probeTmux(ctx context.Context, masterID, home string, profile executorprotocol.TerminalProfile) (executorprotocol.TmuxProbe, error) {
	productionSocket, configPath, shellConfigPath, err := prepareTerminalFiles(masterID, profile)
	if err != nil {
		return executorprotocol.TmuxProbe{}, err
	}
	socketPath := filepath.Join("/tmp", fmt.Sprintf("wi-%d", os.Getuid()), fmt.Sprintf("executor-doctor-%s-%d.sock", shortKey(masterID), os.Getpid()))
	_ = os.Remove(socketPath)
	command := strings.TrimSpace(w.TmuxCommand)
	if command == "" {
		command = "tmux"
	}
	client := tmux.NewWithServer(command, socketPath, configPath)
	created, err := client.EnsureSession(ctx, tmux.SessionSpec{Name: "wi-doctor", CWD: home, Env: profile.Environment})
	if err != nil {
		return executorprotocol.TmuxProbe{}, fmt.Errorf("start dedicated worker tmux server: %w", err)
	}
	if !created {
		return executorprotocol.TmuxProbe{}, fmt.Errorf("worker tmux probe unexpectedly reused a session")
	}
	defer func() { _ = client.KillServer(context.Background()); _ = os.Remove(socketPath) }()
	defaultShell, err := client.GlobalOption(ctx, "default-shell")
	if err != nil {
		return executorprotocol.TmuxProbe{}, fmt.Errorf("inspect worker tmux default shell: %w", err)
	}
	defaultCommand, err := client.GlobalOption(ctx, "default-command")
	if err != nil {
		return executorprotocol.TmuxProbe{}, fmt.Errorf("inspect worker tmux default command: %w", err)
	}
	statusPosition, err := client.GlobalOption(ctx, "status-position")
	if err != nil {
		return executorprotocol.TmuxProbe{}, fmt.Errorf("inspect worker tmux status position: %w", err)
	}
	production := tmux.NewWithServer(command, productionSocket, configPath)
	if sessions, listErr := production.ListSessions(ctx); listErr != nil {
		return executorprotocol.TmuxProbe{}, fmt.Errorf("inspect dedicated worker tmux server: %w", listErr)
	} else if len(sessions) > 0 {
		if err := production.SourceFile(ctx, configPath); err != nil {
			return executorprotocol.TmuxProbe{}, fmt.Errorf("reload dedicated worker tmux configuration: %w", err)
		}
	}
	return executorprotocol.TmuxProbe{SocketPath: productionSocket, ConfigPath: configPath, ShellConfigPath: shellConfigPath, DefaultShell: defaultShell, DefaultCommand: defaultCommand, StatusPosition: statusPosition}, nil
}

func (w Worker) ensureWorkspace(ctx context.Context, request executorprotocol.Request) (executorprotocol.Response, error) {
	if request.Workspace == nil || !model.ValidID(request.Workspace.ItemID) {
		return executorprotocol.Response{}, fmt.Errorf("valid workspace request is required")
	}
	workspace := request.Workspace
	if !validExecutorName(workspace.ExecutorName) {
		return executorprotocol.Response{}, fmt.Errorf("workspace executor name is invalid")
	}
	if !safePathComponent(workspace.RepositoryKey) || !safePathComponent(workspace.Slot) {
		return executorprotocol.Response{}, fmt.Errorf("workspace repository and slot identities must be safe path components")
	}
	if len(workspace.GitBundle) == 0 || len(workspace.GitBundle) > 512<<20 {
		return executorprotocol.Response{}, fmt.Errorf("workspace Git bundle is empty or exceeds 512 MiB")
	}
	root, err := workerRoot(request.MasterID)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	repositoryLock, err := itemlock.Acquire(ctx, filepath.Join(root, "locks", "repository-"+workspace.RepositoryKey+".lock"))
	if err != nil {
		return executorprotocol.Response{}, err
	}
	defer repositoryLock.Release()
	claimPath := filepath.Join(root, "claims", workspace.RepositoryKey, workspace.Slot)
	if owner, readErr := os.ReadFile(claimPath); readErr == nil && strings.TrimSpace(string(owner)) != workspace.ItemID {
		return executorprotocol.Response{}, fmt.Errorf("remote managed slot %s is claimed by work item %s", workspace.Slot, strings.TrimSpace(string(owner)))
	} else if readErr != nil && !os.IsNotExist(readErr) {
		return executorprotocol.Response{}, readErr
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return executorprotocol.Response{}, err
	}
	repository := filepath.Join(root, "repositories", workspace.RepositoryKey+".git")
	if err := os.MkdirAll(filepath.Dir(repository), 0o700); err != nil {
		return executorprotocol.Response{}, err
	}
	if _, err := os.Stat(repository); os.IsNotExist(err) {
		if _, err := runCommand(ctx, "git", "init", "--bare", repository); err != nil {
			return executorprotocol.Response{}, err
		}
	}
	bundle, err := os.CreateTemp(root, "incoming-*.bundle")
	if err != nil {
		return executorprotocol.Response{}, err
	}
	bundlePath := bundle.Name()
	if _, err := bundle.Write(workspace.GitBundle); err != nil {
		_ = bundle.Close()
		_ = os.Remove(bundlePath)
		return executorprotocol.Response{}, err
	}
	if err := bundle.Close(); err != nil {
		_ = os.Remove(bundlePath)
		return executorprotocol.Response{}, err
	}
	defer os.Remove(bundlePath)
	if _, err := runCommand(ctx, "git", "-C", repository, "fetch", "--force", "--update-head-ok", bundlePath, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
		return executorprotocol.Response{}, fmt.Errorf("import repository bundle: %w", err)
	}
	checkoutPath := filepath.Join(workerDataRoot(root), "worktrees", workspace.RepositoryKey, workspace.Slot)
	if _, err := os.Stat(checkoutPath); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(checkoutPath), 0o700); err != nil {
			return executorprotocol.Response{}, err
		}
		if _, err := runCommand(ctx, "git", "-C", repository, "worktree", "add", "-b", workspace.Branch, checkoutPath, workspace.CreatedFromCommit); err != nil {
			if _, retryErr := runCommand(ctx, "git", "-C", repository, "worktree", "add", checkoutPath, workspace.Branch); retryErr != nil {
				return executorprotocol.Response{}, fmt.Errorf("create remote managed slot: %w", err)
			}
		}
	} else if err == nil {
		status, statusErr := runCommand(ctx, "git", "-C", checkoutPath, "status", "--porcelain")
		if statusErr != nil {
			return executorprotocol.Response{}, statusErr
		}
		if strings.TrimSpace(string(status)) != "" {
			return executorprotocol.Response{}, fmt.Errorf("remote managed slot %s is dirty and cannot be reused", workspace.Slot)
		}
		if _, branchErr := runCommand(ctx, "git", "-C", repository, "show-ref", "--verify", "--quiet", "refs/heads/"+workspace.Branch); branchErr == nil {
			if _, err := runCommand(ctx, "git", "-C", checkoutPath, "switch", workspace.Branch); err != nil {
				return executorprotocol.Response{}, err
			}
		} else if _, err := runCommand(ctx, "git", "-C", checkoutPath, "switch", "-c", workspace.Branch, workspace.CreatedFromCommit); err != nil {
			return executorprotocol.Response{}, err
		}
	} else {
		return executorprotocol.Response{}, fmt.Errorf("inspect remote managed slot: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(claimPath), 0o700); err != nil {
		return executorprotocol.Response{}, err
	}
	if err := writeFileAtomic(claimPath, []byte(workspace.ItemID+"\n"), 0o600); err != nil {
		return executorprotocol.Response{}, err
	}
	var manifest model.Manifest
	if err := json.Unmarshal(workspace.ManifestJSON, &manifest); err != nil {
		return executorprotocol.Response{}, fmt.Errorf("decode work item manifest: %w", err)
	}
	desiredState := manifest.State
	manifest.State = model.StateBacklog
	manifest.StateChangedAt = manifest.CreatedAt
	manifest.Repository.CurrentRoot = repository
	manifest.Checkout.Path = &checkoutPath
	manifest.Checkout.Executor = "local"
	if err := writeWorkerConfig(root, workspace.Config); err != nil {
		return executorprotocol.Response{}, err
	}
	if err := writeWorkerAuthorityContext(root, workspace.ExecutorName); err != nil {
		return executorprotocol.Response{}, err
	}
	if request.Terminal != nil {
		if _, _, _, err := prepareTerminalFiles(request.MasterID, *request.Terminal); err != nil {
			return executorprotocol.Response{}, err
		}
	}
	if _, err := runWorkerWI(ctx, root, request.MasterID, "daemon", "start", "--json"); err != nil {
		return executorprotocol.Response{}, err
	}
	paths, err := workerPaths(root, request.MasterID)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	socketPath, err := coordinator.SocketPath(paths.RuntimeDir, paths.DataRoot)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	client := &coordinator.Client{SocketPath: socketPath}
	if _, err := client.CanonicalManifest(ctx, manifest.ID); err != nil {
		_ = os.RemoveAll(filepath.Join(paths.ItemsDir, manifest.ID))
		commandID, idErr := model.NewID()
		if idErr != nil {
			return executorprotocol.Response{}, idErr
		}
		_, createErr := client.CreateItem(ctx, coordinator.CreateItemRequest{Command: coordinator.CreateItemCommand{ID: commandID, ProtocolVersion: coordinator.ProtocolVersion, Manifest: manifest, DescriptionDigest: coordinator.DescriptionDigest(workspace.Description), Actor: "user", CreatedAt: manifest.CreatedAt}, Description: workspace.Description})
		if createErr != nil {
			return executorprotocol.Response{}, fmt.Errorf("create worker item replica: %w", createErr)
		}
	}
	if desiredState == model.StateWorking {
		if _, err := runWorkerWI(ctx, root, request.MasterID, "state", "set", model.StateWorking, "--item", manifest.ID, "--json"); err != nil {
			return executorprotocol.Response{}, fmt.Errorf("activate worker item replica: %w", err)
		}
	}
	return executorprotocol.Response{Workspace: &executorprotocol.WorkspaceResult{Path: checkoutPath, Slot: workspace.Slot, Source: workspace.CreatedFromCommit}}, nil
}

func (w Worker) workspaceStatus(ctx context.Context, request executorprotocol.Request) (executorprotocol.Response, error) {
	if request.Workspace == nil {
		return executorprotocol.Response{}, fmt.Errorf("workspace request is required")
	}
	root, err := workerRoot(request.MasterID)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	output, err := runWorkerWI(ctx, root, request.MasterID, "workspace", "status", "--item", request.Workspace.ItemID, "--json")
	if err != nil {
		return executorprotocol.Response{}, err
	}
	return executorprotocol.Response{Workspace: &executorprotocol.WorkspaceResult{StatusJSON: output}}, nil
}

func (w Worker) workspaceRelease(ctx context.Context, request executorprotocol.Request) (executorprotocol.Response, error) {
	if request.Workspace == nil {
		return executorprotocol.Response{}, fmt.Errorf("workspace request is required")
	}
	root, err := workerRoot(request.MasterID)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	paths, err := workerPaths(root, request.MasterID)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	socket, err := coordinator.SocketPath(paths.RuntimeDir, paths.DataRoot)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	client := &coordinator.Client{SocketPath: socket}
	before, err := client.CanonicalManifest(ctx, request.Workspace.ItemID)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	bundle, err := os.CreateTemp(root, "outgoing-*.bundle")
	if err != nil {
		return executorprotocol.Response{}, err
	}
	bundlePath := bundle.Name()
	_ = bundle.Close()
	defer os.Remove(bundlePath)
	if _, err := runCommand(ctx, "git", "-C", before.Manifest.Repository.OperationalRoot(), "bundle", "create", bundlePath, "--all"); err != nil {
		return executorprotocol.Response{}, err
	}
	gitBundle, err := os.ReadFile(bundlePath)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	args := []string{"workspace", "release", "--item", request.Workspace.ItemID, "--json"}
	if request.Workspace.Force {
		args = append(args, "--force")
	}
	if _, err := runWorkerWI(ctx, root, request.MasterID, args...); err != nil {
		return executorprotocol.Response{}, err
	}
	canonical, err := client.CanonicalManifest(ctx, request.Workspace.ItemID)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	manifestJSON, _ := json.Marshal(canonical.Manifest)
	if safePathComponent(request.Workspace.RepositoryKey) && safePathComponent(request.Workspace.Slot) {
		claimPath := filepath.Join(root, "claims", request.Workspace.RepositoryKey, request.Workspace.Slot)
		if owner, readErr := os.ReadFile(claimPath); readErr == nil && strings.TrimSpace(string(owner)) == request.Workspace.ItemID {
			_ = os.Remove(claimPath)
		}
	}
	return executorprotocol.Response{Workspace: &executorprotocol.WorkspaceResult{Changed: true, ManifestJSON: manifestJSON, GitBundle: gitBundle}}, nil
}

func (w Worker) runtimeEnsure(ctx context.Context, request executorprotocol.Request) (executorprotocol.Response, error) {
	if request.Runtime == nil {
		return executorprotocol.Response{}, fmt.Errorf("runtime request is required")
	}
	root, err := workerRoot(request.MasterID)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	mode := strings.TrimSpace(request.Runtime.Mode)
	if mode != "tui" && mode != "headless" {
		return executorprotocol.Response{}, fmt.Errorf("runtime mode must be tui or headless")
	}
	created := true
	if existing, existingErr := w.runtimeResult(root, request.MasterID, request.Runtime.ItemID, false); existingErr == nil {
		var runtime model.AgentRuntime
		if json.Unmarshal(existing.RuntimeJSON, &runtime) == nil && (runtime.State == model.AgentRuntimeStarting || runtime.State == model.AgentRuntimeRunning) {
			created = false
		}
	}
	if _, err := runWorkerWI(ctx, root, request.MasterID, "agent", "runtime", "ensure", "--mode", mode, "--item", request.Runtime.ItemID, "--json"); err != nil {
		return executorprotocol.Response{}, err
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		result, err := w.runtimeResult(root, request.MasterID, request.Runtime.ItemID, false)
		if err == nil {
			var runtime model.AgentRuntime
			if json.Unmarshal(result.RuntimeJSON, &runtime) == nil && (runtime.Capabilities.Prompt || runtime.State == model.AgentRuntimeProblem) {
				final, finalErr := w.runtimeResult(root, request.MasterID, request.Runtime.ItemID, true)
				if finalErr != nil {
					return executorprotocol.Response{}, finalErr
				}
				final.Changed = created
				return executorprotocol.Response{Runtime: &final}, nil
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	result, err := w.runtimeResult(root, request.MasterID, request.Runtime.ItemID, true)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	var runtime model.AgentRuntime
	if json.Unmarshal(result.RuntimeJSON, &runtime) == nil && !runtime.Capabilities.Prompt && runtime.State != model.AgentRuntimeProblem {
		return executorprotocol.Response{}, fmt.Errorf("worker runtime %s did not become ready within 30s", runtime.ID)
	}
	result.Changed = created
	return executorprotocol.Response{Runtime: &result}, nil
}

func (w Worker) runtimeStatus(ctx context.Context, request executorprotocol.Request) (executorprotocol.Response, error) {
	if request.Runtime == nil {
		return executorprotocol.Response{}, fmt.Errorf("runtime request is required")
	}
	root, err := workerRoot(request.MasterID)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	result, err := w.runtimeResult(root, request.MasterID, request.Runtime.ItemID, true)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	result.StatusJSON, err = runWorkerWI(ctx, root, request.MasterID, "agent", "status", "--item", request.Runtime.ItemID, "--json")
	return executorprotocol.Response{Runtime: &result}, err
}

func (w Worker) runtimeResult(root, masterID, itemID string, includeConversation bool) (executorprotocol.RuntimeResult, error) {
	paths, err := workerPaths(root, masterID)
	if err != nil {
		return executorprotocol.RuntimeResult{}, err
	}
	socketPath, err := coordinator.SocketPath(paths.RuntimeDir, paths.DataRoot)
	if err != nil {
		return executorprotocol.RuntimeResult{}, err
	}
	client := &coordinator.Client{SocketPath: socketPath}
	resources, err := client.ItemResources(context.Background(), itemID)
	if err != nil {
		return executorprotocol.RuntimeResult{}, err
	}
	if resources.Runtime == nil {
		return executorprotocol.RuntimeResult{}, fmt.Errorf("worker runtime is absent")
	}
	canonical, err := client.CanonicalManifest(context.Background(), itemID)
	if err != nil {
		return executorprotocol.RuntimeResult{}, err
	}
	events, err := client.RuntimeEvents(context.Background(), coordinator.RuntimeEventsRequest{ItemID: itemID, Limit: 500})
	if err != nil {
		return executorprotocol.RuntimeResult{}, err
	}
	runtimeJSON, _ := json.Marshal(resources.Runtime)
	manifestJSON, _ := json.Marshal(canonical.Manifest)
	eventsJSON, _ := json.Marshal(events)
	result := executorprotocol.RuntimeResult{RuntimeJSON: runtimeJSON, ManifestJSON: manifestJSON, EventsJSON: eventsJSON}
	if conversation := canonical.Manifest.PrimaryConversation; includeConversation && conversation != nil && conversation.Driver == "pi" {
		locator := filepath.Clean(filepath.FromSlash(conversation.NativeLocator()))
		if !filepath.IsAbs(locator) && locator != "." && locator != ".." && !strings.HasPrefix(locator, ".."+string(filepath.Separator)) {
			self, selfErr := os.Executable()
			if selfErr != nil {
				return executorprotocol.RuntimeResult{}, selfErr
			}
			backend := agentdriver.Client{Driver: "pi", Path: self, Args: []string{"internal-driver", "pi"}, Env: workerEnvironment(root, masterID)}
			data, exportErr := backend.ExportConversation(context.Background(), filepath.Join(paths.ItemsDir, itemID, locator))
			if exportErr != nil {
				return executorprotocol.RuntimeResult{}, exportErr
			}
			result.Conversation = &executorprotocol.ConversationSnapshot{Driver: conversation.Driver, ID: conversation.ID, Locator: conversation.NativeLocator(), Format: conversation.Format, Data: data}
		}
	}
	return result, nil
}

func (w Worker) runtimeControl(ctx context.Context, request executorprotocol.Request) (executorprotocol.Response, error) {
	if request.Runtime == nil {
		return executorprotocol.Response{}, fmt.Errorf("runtime request is required")
	}
	root, err := workerRoot(request.MasterID)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	args := []string{"agent", "control"}
	switch request.Runtime.CommandType {
	case "prompt", "steer":
		args = append(args, "send", "--type", request.Runtime.CommandType, "--item", request.Runtime.ItemID, "--json", request.Runtime.Message)
	case "follow_up":
		args = append(args, "send", "--follow-up", "--item", request.Runtime.ItemID, "--json", request.Runtime.Message)
	case "abort", "shutdown":
		args = append(args, request.Runtime.CommandType, "--item", request.Runtime.ItemID, "--json")
	default:
		return executorprotocol.Response{}, fmt.Errorf("unsupported runtime control %q", request.Runtime.CommandType)
	}
	output, err := runWorkerWI(ctx, root, request.MasterID, args...)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	result, err := w.runtimeResult(root, request.MasterID, request.Runtime.ItemID, true)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	result.ControlJSON = output
	return executorprotocol.Response{Runtime: &result}, nil
}

func (w Worker) terminalClose(ctx context.Context, request executorprotocol.Request) (executorprotocol.Response, error) {
	if request.Runtime == nil {
		return executorprotocol.Response{}, fmt.Errorf("terminal request is required")
	}
	root, err := workerRoot(request.MasterID)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := runWorkerWI(ctx, root, request.MasterID, "terminal", "close", "--item", request.Runtime.ItemID, "--json"); err == nil {
			break
		} else if !time.Now().Before(deadline) {
			return executorprotocol.Response{}, err
		}
		time.Sleep(50 * time.Millisecond)
	}
	return executorprotocol.Response{Runtime: &executorprotocol.RuntimeResult{Changed: true}}, nil
}

func (w Worker) shutdown(ctx context.Context, request executorprotocol.Request) (executorprotocol.Response, error) {
	root, err := workerRoot(request.MasterID)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	if _, err := runWorkerWI(ctx, root, request.MasterID, "shutdown", "--force", "--json"); err != nil {
		return executorprotocol.Response{}, err
	}
	paths, err := workerPaths(root, request.MasterID)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	socket, err := coordinator.SocketPath(paths.RuntimeDir, paths.DataRoot)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	client := &coordinator.Client{SocketPath: socket}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		pingErr := client.Ping(probeCtx)
		cancel()
		if pingErr != nil {
			return executorprotocol.Response{Runtime: &executorprotocol.RuntimeResult{Changed: true}}, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return executorprotocol.Response{}, fmt.Errorf("worker daemon did not stop within 5s")
}

func workerRoot(masterID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "wi-executor", shortKey(masterID)), nil
}

func workerDataRoot(root string) string { return filepath.Join(root, "xdg", "data", "wi") }

func workerPaths(root, masterID string) (xdg.Paths, error) {
	runtimeRoot := filepath.Join(os.TempDir(), fmt.Sprintf("wi-executor-%d", os.Getuid()), shortKey(masterID))
	return xdg.Resolve(xdg.Map{"XDG_DATA_HOME": filepath.Join(root, "xdg", "data"), "XDG_CONFIG_HOME": filepath.Join(root, "xdg", "config"), "XDG_STATE_HOME": filepath.Join(root, "xdg", "state"), "XDG_CACHE_HOME": filepath.Join(root, "xdg", "cache"), "XDG_RUNTIME_DIR": runtimeRoot}, "")
}

func writeWorkerConfig(root, content string) error {
	path := filepath.Join(root, "xdg", "config", "wi", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(content), 0o600)
}

type workerAuthorityContext struct {
	Executor string `json:"executor"`
}

func writeWorkerAuthorityContext(root, executorName string) error {
	encoded, err := json.Marshal(workerAuthorityContext{Executor: executorName})
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(root, "authority.json"), encoded, 0o600)
}

func workerAuthoritySocket(root string) string { return filepath.Join(root, "authority.sock") }

func workerEnvironment(root, masterID string) []string {
	paths, _ := workerPaths(root, masterID)
	_ = os.MkdirAll(paths.RuntimeDir, 0o700)
	values := map[string]string{
		"XDG_DATA_HOME":   paths.DataHome,
		"XDG_CONFIG_HOME": paths.ConfigHome,
		"XDG_STATE_HOME":  paths.StateHome,
		"XDG_CACHE_HOME":  paths.CacheHome,
		"XDG_RUNTIME_DIR": paths.RuntimeDir,
		"WI_TMUX_SOCKET":  filepath.Join(paths.RuntimeDir, "tmux.sock"),
		"WI_TMUX_CONFIG":  filepath.Join(mustUserCacheDir(), "wi", "executors", shortKey(masterID), "tmux.conf"),
	}
	if content, err := os.ReadFile(filepath.Join(root, "authority.json")); err == nil {
		var authority workerAuthorityContext
		if json.Unmarshal(content, &authority) == nil && validExecutorName(authority.Executor) {
			values["WI_EXECUTOR_DAEMON_SOCKET"] = workerAuthoritySocket(root)
			values["WI_EXECUTOR"] = authority.Executor
		}
	}
	profilePath := filepath.Join(mustUserCacheDir(), "wi", "executors", shortKey(masterID), "terminal-profile.json")
	if content, err := os.ReadFile(profilePath); err == nil {
		var profile executorprotocol.TerminalProfile
		if json.Unmarshal(content, &profile) == nil {
			for key, value := range profile.Environment {
				values[key] = value
			}
			if strings.TrimSpace(profile.Shell) != "" {
				values["SHELL"] = profile.Shell
			}
		}
	}
	base := os.Environ()
	env := make([]string, 0, len(base)+len(values))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, replaced := values[key]; replaced {
				continue
			}
		}
		env = append(env, entry)
	}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}

func runWorkerWI(ctx context.Context, root, masterID string, args ...string) ([]byte, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, self, args...)
	cmd.Env = workerEnvironment(root, masterID)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("worker wi %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return out, nil
}

func prepareTerminalFiles(masterID string, profile executorprotocol.TerminalProfile) (string, string, string, error) {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return "", "", "", err
	}
	stateDir := filepath.Join(cacheRoot, "wi", "executors", shortKey(masterID))
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", "", "", err
	}
	shellConfigPath := ""
	if profile.ShellConfig != "" {
		shellConfigPath = filepath.Join(stateDir, "shell.rc")
		if err := writeFileAtomic(shellConfigPath, []byte(profile.ShellConfig), 0o600); err != nil {
			return "", "", "", err
		}
	}
	configPath := filepath.Join(stateDir, "tmux.conf")
	config := strings.TrimRight(profile.TmuxConfig, "\n") + "\n"
	if shell := strings.TrimSpace(profile.Shell); shell != "" {
		config += "set-option -g default-shell " + tmuxConfigQuote(shell) + "\n"
		if shellConfigPath != "" && strings.Contains(strings.ToLower(filepath.Base(shell)), "bash") {
			command, err := tmux.ShellCommand([]string{shell, "--rcfile", shellConfigPath, "-i"})
			if err != nil {
				return "", "", "", err
			}
			config += "set-option -g default-command " + tmuxConfigQuote(command) + "\n"
		}
	}
	if err := writeFileAtomic(configPath, []byte(config), 0o600); err != nil {
		return "", "", "", err
	}
	profileJSON, _ := json.Marshal(profile)
	if err := writeFileAtomic(filepath.Join(stateDir, "terminal-profile.json"), profileJSON, 0o600); err != nil {
		return "", "", "", err
	}
	runtimeDir := filepath.Join(os.TempDir(), fmt.Sprintf("wi-executor-%d", os.Getuid()), shortKey(masterID))
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return "", "", "", err
	}
	return filepath.Join(runtimeDir, "tmux.sock"), configPath, shellConfigPath, nil
}

func mustUserCacheDir() string {
	root, err := os.UserCacheDir()
	if err != nil {
		return os.TempDir()
	}
	return root
}

func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".wi-executor-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func runCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %s: %w", path, strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return out, nil
}

func validExecutorName(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 128 && !strings.ContainsAny(value, "\r\n\x00")
}

func safePathComponent(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

func validateMasterID(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "/\\\r\n\x00") || value == "." || value == ".." {
		return fmt.Errorf("invalid executor master identity")
	}
	return nil
}

func shortKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func tmuxConfigQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func toolVersion(ctx context.Context, name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return "missing"
	}
	args := []string{"--version"}
	if name == "tmux" {
		args = []string{"-V"}
	}
	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err != nil {
		return path + ": " + err.Error()
	}
	line := strings.TrimSpace(string(out))
	if before, _, ok := strings.Cut(line, "\n"); ok {
		line = before
	}
	return path + ": " + line
}
