package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	executorprotocol "github.com/regb/workitem/executor/protocol"
	agentdriver "github.com/regb/workitem/internal/agentdriver"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/coordinator"
	itemlock "github.com/regb/workitem/internal/lock"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
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
	case executorprotocol.OperationItemDelete:
		return w.itemDelete(ctx, request)
	case executorprotocol.OperationItemsReconcile:
		return w.itemsReconcile(ctx, request)
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
	case executorprotocol.OperationTerminalCopyMode:
		return w.terminalCopyMode(ctx, request)
	case executorprotocol.OperationShutdown:
		return w.shutdown(ctx, request)
	default:
		return executorprotocol.Response{}, fmt.Errorf("unsupported executor operation %q", request.Operation)
	}
}

func requestItemID(request executorprotocol.Request) string {
	if request.Item != nil {
		return request.Item.ItemID
	}
	if request.Workspace != nil {
		return request.Workspace.ItemID
	}
	if request.Runtime != nil {
		return request.Runtime.ItemID
	}
	return ""
}

func (w Worker) itemsReconcile(ctx context.Context, request executorprotocol.Request) (executorprotocol.Response, error) {
	if request.Items == nil {
		return executorprotocol.Response{}, fmt.Errorf("item reconciliation request is required")
	}
	keep := map[string]bool{}
	for _, itemID := range request.Items.KeepIDs {
		if !model.ValidID(itemID) {
			return executorprotocol.Response{}, fmt.Errorf("invalid retained work item id %q", itemID)
		}
		keep[itemID] = true
	}
	root, err := workerRoot(request.MasterID)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	locked, err := itemlock.Acquire(ctx, filepath.Join(root, "locks", "reconcile.lock"))
	if err != nil {
		return executorprotocol.Response{}, err
	}
	defer locked.Release()
	if _, err := runWorkerWI(ctx, root, request.MasterID, "daemon", "start", "--json"); err != nil {
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
	manifests, err := client.ListManifests(ctx)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	result := executorprotocol.ItemsResult{RemovedIDs: []string{}, BlockedIDs: []string{}}
	for _, manifest := range manifests {
		if keep[manifest.ID] {
			continue
		}
		blocked := manifest.Checkout.Present()
		if resources, resourceErr := client.ItemResources(ctx, manifest.ID); resourceErr != nil {
			return executorprotocol.Response{}, resourceErr
		} else if resources.Runtime != nil && resources.Runtime.State != model.AgentRuntimeStopped {
			statusJSON, statusErr := runWorkerWI(ctx, root, request.MasterID, "agent", "status", "--item", manifest.ID, "--json")
			if statusErr != nil {
				return executorprotocol.Response{}, statusErr
			}
			var status struct {
				Process struct {
					Online bool `json:"online"`
				} `json:"process"`
			}
			if json.Unmarshal(statusJSON, &status) != nil || status.Process.Online {
				blocked = true
			}
		}
		if sessions, sessionErr := executorTerminalSessions(ctx, request.MasterID, manifest.ID); sessionErr != nil {
			return executorprotocol.Response{}, sessionErr
		} else if len(sessions) > 0 {
			blocked = true
		}
		if blocked {
			result.BlockedIDs = append(result.BlockedIDs, manifest.ID)
			continue
		}
		itemLocked, err := itemlock.Acquire(ctx, filepath.Join(root, "locks", manifest.ID+".lock"))
		if err != nil {
			return executorprotocol.Response{}, err
		}
		_, deleteErr := w.itemDelete(ctx, executorprotocol.Request{MasterID: request.MasterID, Item: &executorprotocol.ItemRequest{ItemID: manifest.ID}})
		releaseErr := itemLocked.Release()
		if deleteErr != nil {
			return executorprotocol.Response{}, deleteErr
		}
		if releaseErr != nil {
			return executorprotocol.Response{}, releaseErr
		}
		result.RemovedIDs = append(result.RemovedIDs, manifest.ID)
	}
	sort.Strings(result.RemovedIDs)
	sort.Strings(result.BlockedIDs)
	return executorprotocol.Response{Items: &result}, nil
}

func (w Worker) itemDelete(ctx context.Context, request executorprotocol.Request) (executorprotocol.Response, error) {
	if request.Item == nil || !model.ValidID(request.Item.ItemID) {
		return executorprotocol.Response{}, fmt.Errorf("valid item deletion request is required")
	}
	root, err := workerRoot(request.MasterID)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	paths, err := workerPaths(root, request.MasterID)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	if _, err := os.Stat(filepath.Join(paths.ItemsDir, request.Item.ItemID)); os.IsNotExist(err) {
		_ = closeExecutorTerminalSessions(ctx, request.MasterID, request.Item.ItemID)
		_ = removeWorkerClaims(root, request.Item.ItemID)
		return executorprotocol.Response{Item: &executorprotocol.ItemResult{}}, nil
	} else if err != nil {
		return executorprotocol.Response{}, err
	}
	socket, err := coordinator.SocketPath(paths.RuntimeDir, paths.DataRoot)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	canonical, err := (&coordinator.Client{SocketPath: socket}).CanonicalManifest(ctx, request.Item.ItemID)
	if err != nil {
		if coordinatorNotFound(err) {
			return executorprotocol.Response{Item: &executorprotocol.ItemResult{}}, nil
		}
		return executorprotocol.Response{}, err
	}
	if canonical.Manifest.Checkout.Present() {
		return executorprotocol.Response{}, fmt.Errorf("worker item still has a workspace claim")
	}
	if err := closeExecutorTerminalSessions(ctx, request.MasterID, request.Item.ItemID); err != nil {
		return executorprotocol.Response{}, err
	}
	if canonical.Manifest.State != model.StateArchived {
		if _, err := runWorkerWI(ctx, root, request.MasterID, "state", "set", model.StateArchived, "--item", request.Item.ItemID, "--json"); err != nil {
			return executorprotocol.Response{}, err
		}
	}
	if _, err := runWorkerWI(ctx, root, request.MasterID, "delete", "--yes", "--item", request.Item.ItemID); err != nil {
		return executorprotocol.Response{}, err
	}
	if err := removeWorkerClaims(root, request.Item.ItemID); err != nil {
		return executorprotocol.Response{}, err
	}
	return executorprotocol.Response{Item: &executorprotocol.ItemResult{Changed: true}}, nil
}

func removeWorkerClaims(root, itemID string) error {
	claimsRoot := filepath.Join(root, "claims")
	return filepath.WalkDir(claimsRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		owner, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(owner)) == itemID {
			return os.Remove(path)
		}
		return nil
	})
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
	socketPath := workerAuthoritySocket(root)
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return executorprotocol.Response{}, fmt.Errorf("remove stale executor authority socket: %w", err)
	}
	return executorprotocol.Response{Authority: &executorprotocol.AuthorityResult{SocketPath: socketPath}}, nil
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
	productionSocket, configPath, err := prepareTerminalFiles(masterID, profile)
	if err != nil {
		return executorprotocol.TmuxProbe{}, err
	}
	socketPath := filepath.Join("/tmp", fmt.Sprintf("wi-%d", os.Getuid()), fmt.Sprintf("executor-doctor-%s-%d.sock", shortKey(masterID), os.Getpid()))
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return executorprotocol.TmuxProbe{}, fmt.Errorf("create executor doctor runtime directory: %w", err)
	}
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
	production := tmux.NewWithServer(command, productionSocket, configPath)
	if sessions, listErr := production.ListSessions(ctx); listErr != nil {
		return executorprotocol.TmuxProbe{}, fmt.Errorf("inspect dedicated worker tmux server: %w", listErr)
	} else if len(sessions) > 0 {
		if err := production.SourceFile(ctx, configPath); err != nil {
			return executorprotocol.TmuxProbe{}, fmt.Errorf("reload dedicated worker tmux configuration: %w", err)
		}
	}
	return executorprotocol.TmuxProbe{SocketPath: productionSocket, ConfigPath: configPath, DefaultShell: defaultShell}, nil
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
	if err := importCoordinatorBundle(ctx, repository, bundlePath); err != nil {
		return executorprotocol.Response{}, err
	}
	startPoint := workspace.CreatedFromCommit
	coordinatorBranch := "refs/remotes/wi-coordinator/" + workspace.Branch
	if _, err := runCommand(ctx, "git", "-C", repository, "show-ref", "--verify", "--quiet", coordinatorBranch); err == nil {
		startPoint = coordinatorBranch
	}
	checkoutPath := filepath.Join(workerDataRoot(root), "worktrees", workspace.RepositoryKey, workspace.Slot)
	if _, err := os.Stat(checkoutPath); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(checkoutPath), 0o700); err != nil {
			return executorprotocol.Response{}, err
		}
		if _, err := runCommand(ctx, "git", "-C", repository, "worktree", "add", "-B", workspace.Branch, checkoutPath, startPoint); err != nil {
			return executorprotocol.Response{}, fmt.Errorf("create remote managed slot: %w", err)
		}
	} else if err == nil {
		status, statusErr := runCommand(ctx, "git", "-C", checkoutPath, "status", "--porcelain")
		if statusErr != nil {
			return executorprotocol.Response{}, statusErr
		}
		if strings.TrimSpace(string(status)) != "" {
			return executorprotocol.Response{}, fmt.Errorf("remote managed slot %s is dirty and cannot be reused", workspace.Slot)
		}
		if _, err := runCommand(ctx, "git", "-C", checkoutPath, "switch", "-C", workspace.Branch, startPoint); err != nil {
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
	manifest.Repository.CurrentRoot = repository
	manifest.Checkout.Specification.Executor = model.ExecutorLocal
	if manifest.PrimaryConversation != nil {
		conversation := *manifest.PrimaryConversation
		conversation.Executor = model.ExecutorLocal
		manifest.PrimaryConversation = &conversation
	}
	claim, err := manifest.Checkout.AssignNewClaim(model.ExecutorLocal, workspace.RepositoryKey, workspace.Slot, checkoutPath)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	if err := writeWorkerConfig(root, workspace.Config); err != nil {
		return executorprotocol.Response{}, err
	}
	if err := writeWorkerAuthorityContext(root, workspace.ExecutorName); err != nil {
		return executorprotocol.Response{}, err
	}
	if request.Terminal != nil {
		if _, _, err := prepareTerminalFiles(request.MasterID, *request.Terminal); err != nil {
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
	canonical, lookupErr := client.CanonicalManifest(ctx, manifest.ID)
	if lookupErr != nil {
		if !coordinatorNotFound(lookupErr) {
			return executorprotocol.Response{}, lookupErr
		}
		_ = os.RemoveAll(filepath.Join(paths.ItemsDir, manifest.ID))
		commandID, idErr := model.NewID()
		if idErr != nil {
			return executorprotocol.Response{}, idErr
		}
		initial := manifest
		initial.State = model.StateBacklog
		initial.StateChangedAt = initial.CreatedAt
		created, createErr := client.CreateItem(ctx, coordinator.CreateItemRequest{Command: coordinator.CreateItemCommand{ID: commandID, ProtocolVersion: coordinator.ProtocolVersion, Manifest: initial, DescriptionDigest: coordinator.DescriptionDigest(workspace.Description), Actor: "user", CreatedAt: manifest.CreatedAt}, Description: workspace.Description})
		if createErr != nil {
			return executorprotocol.Response{}, fmt.Errorf("create worker item replica: %w", createErr)
		}
		canonical = coordinator.CanonicalManifest{Manifest: created.Manifest, Revision: created.Revision}
	}
	if canonical.Manifest.PrimaryConversation != nil {
		if manifest.PrimaryConversation != nil && manifest.PrimaryConversation.ID != canonical.Manifest.PrimaryConversation.ID {
			return executorprotocol.Response{}, fmt.Errorf("worker conversation differs from authoritative conversation")
		}
		manifest.PrimaryConversation = canonical.Manifest.PrimaryConversation
	}
	if err := saveWorkerManifest(ctx, client, canonical, manifest); err != nil {
		return executorprotocol.Response{}, fmt.Errorf("reconcile worker item replica: %w", err)
	}
	if err := store.New(paths.DataRoot).SaveDescription(ctx, manifest.ID, workspace.Description); err != nil {
		return executorprotocol.Response{}, fmt.Errorf("refresh worker item description: %w", err)
	}
	return executorprotocol.Response{Workspace: &executorprotocol.WorkspaceResult{ClaimID: claim.ID, ClaimGeneration: claim.Generation, Executor: workspace.ExecutorName, RepositoryKey: workspace.RepositoryKey, Path: checkoutPath, Slot: workspace.Slot, Source: workspace.CreatedFromCommit}}, nil
}

func importCoordinatorBundle(ctx context.Context, repository, bundlePath string) error {
	if _, err := runCommand(ctx, "git", "-C", repository, "fetch", "--force", bundlePath, "+refs/heads/*:refs/remotes/wi-coordinator/*", "+refs/tags/*:refs/wi-coordinator/tags/*"); err != nil {
		return fmt.Errorf("import repository bundle: %w", err)
	}
	return nil
}

func saveWorkerManifest(ctx context.Context, client *coordinator.Client, canonical coordinator.CanonicalManifest, manifest model.Manifest) error {
	commandID, err := model.NewID()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	_, err = client.ExecuteStoreCommand(ctx, coordinator.StoreCommandRequest{Command: coordinator.StoreCommand{ID: commandID, ProtocolVersion: coordinator.ProtocolVersion, Operation: coordinator.StoreManifestSave, ItemID: manifest.ID, ExpectedRevision: &canonical.Revision, PayloadDigest: coordinator.StorePayloadDigest(payload), CreatedAt: time.Now().UTC()}, Payload: payload})
	return err
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
	terminalSessions, err := executorTerminalSessions(ctx, request.MasterID, request.Workspace.ItemID)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	if len(terminalSessions) > 0 {
		return executorprotocol.Response{}, fmt.Errorf("executor terminal sessions are still present; close the terminal before releasing the workspace")
	}
	before, err := client.CanonicalManifest(ctx, request.Workspace.ItemID)
	if err != nil {
		return executorprotocol.Response{}, err
	}
	claim := before.Manifest.Checkout.Claim
	if claim == nil || strings.TrimSpace(request.Workspace.ClaimID) == "" || claim.ID != request.Workspace.ClaimID || claim.Generation != request.Workspace.ClaimGeneration {
		return executorprotocol.Response{}, fmt.Errorf("workspace claim identity does not match the worker assignment")
	}
	if claim.RepositoryKey != request.Workspace.RepositoryKey || claim.Slot != request.Workspace.Slot {
		return executorprotocol.Response{}, fmt.Errorf("workspace claim location does not match the worker assignment")
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
			if json.Unmarshal(result.RuntimeJSON, &runtime) == nil && (executorRuntimeReady(result.EventsJSON, runtime.ID) || runtime.State == model.AgentRuntimeProblem) {
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
	if json.Unmarshal(result.RuntimeJSON, &runtime) == nil && !executorRuntimeReady(result.EventsJSON, runtime.ID) && runtime.State != model.AgentRuntimeProblem {
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
	if conversation := canonical.Manifest.PrimaryConversation; includeConversation && conversation != nil {
		locator := filepath.Clean(filepath.FromSlash(conversation.NativeLocator()))
		if !filepath.IsAbs(locator) && locator != "." && locator != ".." && !strings.HasPrefix(locator, ".."+string(filepath.Separator)) {
			backend, backendErr := workerDriverClient(root, masterID, conversation.Driver)
			if backendErr != nil {
				return executorprotocol.RuntimeResult{}, backendErr
			}
			data, exportErr := backend.ExportConversation(context.Background(), filepath.Join(paths.ItemsDir, itemID, locator))
			if exportErr != nil {
				return executorprotocol.RuntimeResult{}, exportErr
			}
			result.Conversation = &executorprotocol.ConversationSnapshot{Driver: conversation.Driver, ID: conversation.ID, Locator: conversation.NativeLocator(), Format: conversation.Format, Data: data}
		}
	}
	return result, nil
}

func executorRuntimeReady(raw []byte, runtimeID string) bool {
	var events coordinator.RuntimeEventsResult
	if json.Unmarshal(raw, &events) != nil {
		return false
	}
	for _, event := range events.Events {
		if event.Type != "runtime.ready" {
			continue
		}
		var payload struct {
			RuntimeID string `json:"runtime_id"`
		}
		if json.Unmarshal(event.Payload, &payload) == nil && payload.RuntimeID == runtimeID {
			return true
		}
	}
	return false
}

func workerDriverClient(root, masterID, driver string) (agentdriver.Client, error) {
	cfg, _, err := config.Load(filepath.Join(root, "xdg", "config", "wi", "config.toml"))
	if err != nil {
		return agentdriver.Client{}, err
	}
	definition, ok := cfg.Drivers[driver]
	if !ok {
		return agentdriver.Client{}, fmt.Errorf("worker driver %q is not configured", driver)
	}
	path := strings.TrimSpace(definition.Command)
	args := []string(nil)
	if path == "" && driver == "pi" {
		path, err = os.Executable()
		if err != nil {
			return agentdriver.Client{}, err
		}
		args = []string{"internal-driver", "pi"}
	}
	if path == "" {
		return agentdriver.Client{}, fmt.Errorf("worker driver %q has no command", driver)
	}
	return agentdriver.Client{Driver: driver, Path: path, Args: args, Env: workerEnvironment(root, masterID)}, nil
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

func (w Worker) terminalCopyMode(ctx context.Context, request executorprotocol.Request) (executorprotocol.Response, error) {
	if request.Runtime == nil || !model.ValidID(request.Runtime.ItemID) {
		return executorprotocol.Response{}, fmt.Errorf("valid terminal copy-mode request is required")
	}
	itemID := request.Runtime.ItemID
	target := model.TerminalSessionName(itemID)
	client := executorTmuxClient(request.MasterID)
	if attachmentID := strings.TrimSpace(request.Runtime.AttachmentID); attachmentID != "" {
		if !validGatewayPaneID(attachmentID) {
			return executorprotocol.Response{}, fmt.Errorf("invalid gateway pane identity")
		}
		target = shellSessionName(itemID, attachmentID)
		exists, err := client.HasSession(ctx, target)
		if err != nil {
			return executorprotocol.Response{}, err
		}
		if !exists {
			sessions, err := executorTerminalSessions(ctx, request.MasterID, itemID)
			if err != nil {
				return executorprotocol.Response{}, err
			}
			shells := []string{}
			for _, session := range sessions {
				if strings.HasPrefix(session, shellSessionPrefix(itemID)) {
					shells = append(shells, session)
				}
			}
			if len(shells) != 1 {
				return executorprotocol.Response{}, fmt.Errorf("cannot identify the worker shell for gateway pane %s; close and recreate that shell pane", attachmentID)
			}
			target = shells[0]
		}
	}
	if err := client.CopyMode(ctx, target); err != nil {
		return executorprotocol.Response{}, fmt.Errorf("enter executor terminal copy mode: %w", err)
	}
	return executorprotocol.Response{Runtime: &executorprotocol.RuntimeResult{Changed: true}}, nil
}

func validGatewayPaneID(value string) bool {
	if len(value) < 2 || value[0] != '%' {
		return false
	}
	for _, r := range value[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func shellSessionName(itemID, attachmentID string) string {
	return shellSessionPrefix(itemID) + shortKey(attachmentID)[:8]
}

func (w Worker) terminalClose(ctx context.Context, request executorprotocol.Request) (executorprotocol.Response, error) {
	if request.Runtime == nil || !model.ValidID(request.Runtime.ItemID) {
		return executorprotocol.Response{}, fmt.Errorf("valid terminal request is required")
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
	_, lookupErr := (&coordinator.Client{SocketPath: socket}).CanonicalManifest(ctx, request.Runtime.ItemID)
	if lookupErr == nil {
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := runWorkerWI(ctx, root, request.MasterID, "terminal", "close", "--item", request.Runtime.ItemID, "--json"); err == nil {
				break
			} else if !time.Now().Before(deadline) {
				return executorprotocol.Response{}, err
			}
			time.Sleep(50 * time.Millisecond)
		}
	} else if !coordinatorNotFound(lookupErr) {
		return executorprotocol.Response{}, lookupErr
	}
	if err := closeExecutorTerminalSessions(ctx, request.MasterID, request.Runtime.ItemID); err != nil {
		return executorprotocol.Response{}, err
	}
	return executorprotocol.Response{Runtime: &executorprotocol.RuntimeResult{Changed: true}}, nil
}

func executorTerminalSessions(ctx context.Context, masterID, itemID string) ([]string, error) {
	sessions, err := executorTmuxClient(masterID).ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	matched := []string{}
	for _, session := range sessions {
		if session == model.TerminalSessionName(itemID) || strings.HasPrefix(session, shellSessionPrefix(itemID)) {
			matched = append(matched, session)
		}
	}
	return matched, nil
}

func closeExecutorTerminalSessions(ctx context.Context, masterID, itemID string) error {
	sessions, err := executorTerminalSessions(ctx, masterID, itemID)
	if err != nil {
		return err
	}
	client := executorTmuxClient(masterID)
	for _, session := range sessions {
		if err := client.KillSession(ctx, session); err != nil {
			return err
		}
	}
	return nil
}

func coordinatorNotFound(err error) bool {
	var protocolErr *coordinator.ProtocolError
	return errors.As(err, &protocolErr) && protocolErr.Code == "not_found"
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
			_ = executorTmuxClient(request.MasterID).KillServer(context.Background())
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
		"XDG_DATA_HOME":      paths.DataHome,
		"XDG_CONFIG_HOME":    paths.ConfigHome,
		"XDG_STATE_HOME":     paths.StateHome,
		"XDG_CACHE_HOME":     paths.CacheHome,
		"XDG_RUNTIME_DIR":    paths.RuntimeDir,
		"WI_TMUX_SOCKET":     filepath.Join(paths.RuntimeDir, "tmux.sock"),
		"WI_TMUX_CONFIG":     filepath.Join(mustUserCacheDir(), "wi", "executors", shortKey(masterID), "tmux.conf"),
		"WI_EXECUTOR_WORKER": "1",
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

func prepareTerminalFiles(masterID string, profile executorprotocol.TerminalProfile) (string, string, error) {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return "", "", err
	}
	stateDir := filepath.Join(cacheRoot, "wi", "executors", shortKey(masterID))
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", "", err
	}
	configPath := filepath.Join(stateDir, "tmux.conf")
	config := strings.Join([]string{
		"set-option -g status off",
		"set-option -g prefix None",
		"set-option -g prefix2 None",
		"set-option -g mouse off",
		"set-option -g history-limit 50000",
		"set-option -g extended-keys on",
		"set-option -g set-clipboard on",
		"set-option -g allow-passthrough on",
	}, "\n") + "\n"
	if modeKeys := strings.TrimSpace(profile.ModeKeys); modeKeys != "" {
		if modeKeys != "vi" && modeKeys != "emacs" {
			return "", "", fmt.Errorf("executor tmux mode keys must be vi or emacs")
		}
		config += "set-window-option -g mode-keys " + modeKeys + "\n"
	}
	if shell := strings.TrimSpace(profile.Shell); shell != "" {
		config += "set-option -g default-shell " + tmuxConfigQuote(shell) + "\n"
	}
	if err := writeFileAtomic(configPath, []byte(config), 0o600); err != nil {
		return "", "", err
	}
	profileJSON, _ := json.Marshal(profile)
	if err := writeFileAtomic(filepath.Join(stateDir, "terminal-profile.json"), profileJSON, 0o600); err != nil {
		return "", "", err
	}
	runtimeDir := filepath.Join(os.TempDir(), fmt.Sprintf("wi-executor-%d", os.Getuid()), shortKey(masterID))
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return "", "", err
	}
	return filepath.Join(runtimeDir, "tmux.sock"), configPath, nil
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
