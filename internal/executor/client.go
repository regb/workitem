package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/regb/workitem/internal/model"

	executorprotocol "github.com/regb/workitem/executor/protocol"
	"github.com/regb/workitem/internal/config"
)

type Client struct {
	Name       string
	Definition config.ExecutorConfig
	MasterID   string
}

func (c Client) stopAuthorityTunnel(ctx context.Context) {
	control := c.authorityControlPath()
	sshCommand := c.sshCommand()
	_ = exec.CommandContext(ctx, sshCommand, "-S", control, "-O", "exit", c.Definition.Destination).Run()
	_ = os.Remove(control)
}

func (c Client) authorityControlPath() string {
	sum := sha256.Sum256([]byte(c.Name + "\x00" + c.Definition.Destination))
	return filepath.Join(os.TempDir(), fmt.Sprintf("wi-authority-%d", os.Getuid()), shortHex(c.MasterID)+"-"+hex.EncodeToString(sum[:8])+".sock")
}

func shortHex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func (c Client) EnsureWorkspace(ctx context.Context, manifest model.Manifest, description, repositoryKey, slot, configText string) (executorprotocol.WorkspaceResult, error) {
	bundle, err := os.CreateTemp("", "wi-executor-*.bundle")
	if err != nil {
		return executorprotocol.WorkspaceResult{}, err
	}
	bundlePath := bundle.Name()
	_ = bundle.Close()
	defer os.Remove(bundlePath)
	command := exec.CommandContext(ctx, "git", "-C", manifest.Repository.OperationalRoot(), "bundle", "create", bundlePath, "--all")
	if out, err := command.CombinedOutput(); err != nil {
		return executorprotocol.WorkspaceResult{}, fmt.Errorf("create executor Git bundle: %s: %w", strings.TrimSpace(string(out)), err)
	}
	bundleBytes, err := os.ReadFile(bundlePath)
	if err != nil {
		return executorprotocol.WorkspaceResult{}, err
	}
	manifestJSON, _ := json.Marshal(manifest)
	response, err := c.exchange(ctx, executorprotocol.Request{ProtocolVersion: executorprotocol.Version, Operation: executorprotocol.OperationWorkspaceEnsure, MasterID: c.MasterID, Workspace: &executorprotocol.WorkspaceRequest{ItemID: manifest.ID, ExecutorName: c.Name, Description: description, RepositoryKey: repositoryKey, CreatedFromCommit: manifest.Repository.CreatedFromCommit, Branch: manifest.Checkout.Specification.Branch, Slot: slot, ManifestJSON: manifestJSON, GitBundle: bundleBytes, Config: configText}})
	if err != nil {
		return executorprotocol.WorkspaceResult{}, err
	}
	if response.Workspace == nil {
		return executorprotocol.WorkspaceResult{}, fmt.Errorf("executor returned no workspace result")
	}
	return *response.Workspace, nil
}

func (c Client) ReconcileItems(ctx context.Context, keepIDs []string) (executorprotocol.ItemsResult, error) {
	response, err := c.exchange(ctx, executorprotocol.Request{ProtocolVersion: executorprotocol.Version, Operation: executorprotocol.OperationItemsReconcile, MasterID: c.MasterID, Items: &executorprotocol.ItemsRequest{KeepIDs: keepIDs}})
	if err != nil {
		return executorprotocol.ItemsResult{}, err
	}
	if response.Items == nil {
		return executorprotocol.ItemsResult{}, fmt.Errorf("executor returned no item reconciliation result")
	}
	return *response.Items, nil
}

func (c Client) DeleteItem(ctx context.Context, itemID string) error {
	response, err := c.exchange(ctx, executorprotocol.Request{ProtocolVersion: executorprotocol.Version, Operation: executorprotocol.OperationItemDelete, MasterID: c.MasterID, Item: &executorprotocol.ItemRequest{ItemID: itemID}})
	if err != nil {
		return err
	}
	if response.Item == nil {
		return fmt.Errorf("executor returned no item deletion result")
	}
	return nil
}

func (c Client) WorkspaceStatus(ctx context.Context, itemID string) (executorprotocol.WorkspaceResult, error) {
	response, err := c.exchange(ctx, executorprotocol.Request{ProtocolVersion: executorprotocol.Version, Operation: executorprotocol.OperationWorkspaceStatus, MasterID: c.MasterID, Workspace: &executorprotocol.WorkspaceRequest{ItemID: itemID}})
	if err != nil {
		return executorprotocol.WorkspaceResult{}, err
	}
	if response.Workspace == nil {
		return executorprotocol.WorkspaceResult{}, fmt.Errorf("executor returned no workspace status")
	}
	return *response.Workspace, nil
}

func (c Client) ImportWorkspaceBundle(ctx context.Context, manifest model.Manifest, bundle []byte) error {
	file, err := os.CreateTemp("", "wi-executor-return-*.bundle")
	if err != nil {
		return err
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.Write(bundle); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	refspec := "+refs/heads/" + manifest.Checkout.Specification.Branch + ":refs/heads/" + manifest.Checkout.Specification.Branch
	command := exec.CommandContext(ctx, "git", "-C", manifest.Repository.OperationalRoot(), "fetch", "--force", path, refspec)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("import executor Git bundle: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func (c Client) ReleaseWorkspace(ctx context.Context, manifest model.Manifest, force bool) (executorprotocol.WorkspaceResult, error) {
	claim := manifest.Checkout.Claim
	if claim == nil {
		return executorprotocol.WorkspaceResult{}, fmt.Errorf("workspace claim is required for executor release")
	}
	response, err := c.exchange(ctx, executorprotocol.Request{ProtocolVersion: executorprotocol.Version, Operation: executorprotocol.OperationWorkspaceRelease, MasterID: c.MasterID, Workspace: &executorprotocol.WorkspaceRequest{ItemID: manifest.ID, ClaimID: claim.ID, ClaimGeneration: claim.Generation, RepositoryKey: claim.RepositoryKey, Slot: claim.Slot, Force: force}})
	if err != nil {
		return executorprotocol.WorkspaceResult{}, err
	}
	if response.Workspace == nil {
		return executorprotocol.WorkspaceResult{}, fmt.Errorf("executor returned no workspace release")
	}
	return *response.Workspace, nil
}

func (c Client) ExecuteWorkspaceTool(ctx context.Context, request executorprotocol.ToolRequest) (executorprotocol.ToolResult, error) {
	response, err := c.exchange(ctx, executorprotocol.Request{ProtocolVersion: executorprotocol.Version, Operation: executorprotocol.OperationWorkspaceExec, MasterID: c.MasterID, ExecutorName: c.Name, Tool: &request})
	if err != nil {
		return executorprotocol.ToolResult{}, err
	}
	if response.Tool == nil {
		return executorprotocol.ToolResult{}, fmt.Errorf("executor returned no workspace tool result")
	}
	return *response.Tool, nil
}

func (c Client) Shutdown(ctx context.Context) error {
	_, err := c.exchange(ctx, executorprotocol.Request{ProtocolVersion: executorprotocol.Version, Operation: executorprotocol.OperationShutdown, MasterID: c.MasterID})
	c.stopAuthorityTunnel(context.Background())
	return err
}

func (c Client) Doctor(ctx context.Context) (executorprotocol.DoctorResult, error) {
	response, err := c.exchange(ctx, executorprotocol.Request{ProtocolVersion: executorprotocol.Version, Operation: executorprotocol.OperationDoctor, MasterID: c.MasterID})
	if err != nil {
		return executorprotocol.DoctorResult{}, err
	}
	if response.Doctor == nil {
		return executorprotocol.DoctorResult{}, fmt.Errorf("executor returned no doctor result")
	}
	return *response.Doctor, nil
}

func (c Client) exchange(ctx context.Context, request executorprotocol.Request) (executorprotocol.Response, error) {
	sshCommand := c.sshCommand()
	wiCommand, err := c.wiCommand()
	if err != nil {
		return executorprotocol.Response{}, err
	}
	controlPath := c.executionControlPath()
	if err := os.MkdirAll(filepath.Dir(controlPath), 0o700); err != nil {
		return executorprotocol.Response{}, err
	}
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "ControlMaster=auto", "-o", "ControlPersist=60", "-o", "ControlPath=" + controlPath, c.Definition.Destination, wiCommand, "internal-executor"}
	cmd := exec.CommandContext(ctx, sshCommand, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return executorprotocol.Response{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return executorprotocol.Response{}, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return executorprotocol.Response{}, fmt.Errorf("start executor SSH command: %w", err)
	}
	response, exchangeErr := executorprotocol.Exchange(ctx, stdin, stdout, request)
	waitErr := cmd.Wait()
	if exchangeErr != nil {
		if text := strings.TrimSpace(stderr.String()); text != "" {
			return executorprotocol.Response{}, fmt.Errorf("%w: %s", exchangeErr, text)
		}
		return executorprotocol.Response{}, exchangeErr
	}
	if waitErr != nil {
		if text := strings.TrimSpace(stderr.String()); text != "" {
			return executorprotocol.Response{}, fmt.Errorf("executor SSH command: %s: %w", text, waitErr)
		}
		return executorprotocol.Response{}, fmt.Errorf("executor SSH command: %w", waitErr)
	}
	return response, nil
}

func (c Client) executionControlPath() string {
	sum := sha256.Sum256([]byte(c.Name + "\x00" + c.Definition.Destination))
	return filepath.Join(os.TempDir(), fmt.Sprintf("wi-executor-%d", os.Getuid()), shortHex(c.MasterID)+"-"+hex.EncodeToString(sum[:8])+".sock")
}

func (c Client) sshCommand() string {
	if command := strings.TrimSpace(c.Definition.SSHCommand); command != "" {
		return command
	}
	return "ssh"
}

func (c Client) wiCommand() (string, error) {
	command := strings.TrimSpace(c.Definition.WICommand)
	if command == "" {
		command = "wi"
	}
	if strings.ContainsAny(command, " \t\r\n\x00") {
		return "", fmt.Errorf("executor wi_command must be one executable path without arguments")
	}
	return command, nil
}

func executorRepositoryKey(repository model.Repository) string {
	value := strings.TrimSpace(repository.GitCommonDir)
	if value == "" {
		value = strings.TrimSpace(repository.RemoteURL)
	}
	if value == "" {
		value = strings.TrimSpace(repository.RootAtCreation)
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}
