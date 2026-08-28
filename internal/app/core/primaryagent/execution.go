package primaryagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app/contract"
	itemlock "github.com/regb/workitem/internal/lock"
	"github.com/regb/workitem/internal/model"
)

type ExecutionSpec struct {
	SessionPath       string
	Mode              agent.Mode
	CWD               string
	LogPath           string
	WorkItemID        string
	RuntimeID         string
	ControlSocketPath string
	SessionDir        string
}

type ExecuteBackend func(context.Context, ExecutionSpec) error

func (s *Service) Execute(ctx context.Context, itemID, sessionID, runtimeID string, mode agent.Mode, execute ExecuteBackend) (runErr error) {
	if execute == nil {
		return fmt.Errorf("agent backend adapter is not configured")
	}
	if !mode.Valid() {
		return fmt.Errorf("invalid agent runtime mode %q", mode)
	}
	m, err := s.resolve(ctx, contract.ResolveOptions{Selector: itemID})
	if err != nil {
		return err
	}
	session, err := ResolveConversation(m, sessionID)
	if err != nil {
		return err
	}
	runtime, err := s.store.LoadAgentRuntime(m.ID)
	if err != nil {
		return err
	}
	if runtime == nil || runtime.ID != runtimeID || runtime.ConversationID != session.ID || runtime.Mode != string(mode) {
		return fmt.Errorf("agent runtime %s does not match work item %s conversation %s in %s mode", runtimeID, m.ID, session.ID, mode)
	}
	absolute, err := s.AbsPath(m.ID, session.Path)
	if err != nil {
		return err
	}
	lock, err := itemlock.TryAcquire(s.LockPath(m.ID, session.ID))
	if err != nil {
		if errors.Is(err, itemlock.ErrLocked) {
			return fmt.Errorf("session %s is already running or locked", session.ID)
		}
		return err
	}
	defer lock.Release()
	cwd := ExecutionCWD(m, session)
	now := s.now()
	runtime.State = model.AgentRuntimeRunning
	runtime.HostPID = os.Getpid()
	if s.process == nil {
		return fmt.Errorf("process inspector is not configured")
	}
	identity, err := s.process.Info(runtime.HostPID)
	if err != nil {
		return fmt.Errorf("inspect agent runtime process identity: %w", err)
	}
	runtime.HostProcessGroup = identity.PGRP
	runtime.HostStartTime = identity.StartTime
	runtime.UpdatedAt = now
	if err := s.store.SaveAgentRuntime(ctx, m.ID, *runtime); err != nil {
		return err
	}
	controlSocketPath := s.ControlSocketPath(m.ID, runtime)
	logPath := s.LogPath(m.ID, runtime)
	for _, runtimePath := range []string{controlSocketPath, logPath} {
		if runtimePath == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(runtimePath), 0o700); err != nil {
			return fmt.Errorf("create agent runtime directory: %w", err)
		}
	}
	defer func() {
		if controlSocketPath != "" {
			_ = os.Remove(controlSocketPath)
			_ = os.Remove(filepath.Dir(controlSocketPath))
		}
		stopped := s.now()
		runtime.UpdatedAt = stopped
		runtime.StoppedAt = &stopped
		runtime.State = model.AgentRuntimeStopped
		runtime.HostPID = 0
		runtime.HostProcessGroup = 0
		runtime.HostStartTime = 0
		if runErr != nil {
			runtime.State = model.AgentRuntimeProblem
		}
		_ = s.store.SaveAgentRuntime(context.Background(), m.ID, *runtime)
	}()
	return execute(ctx, ExecutionSpec{SessionPath: absolute, Mode: mode, CWD: cwd, LogPath: logPath, WorkItemID: m.ID, RuntimeID: runtime.ID, ControlSocketPath: controlSocketPath, SessionDir: s.SessionDir(m.ID)})
}
