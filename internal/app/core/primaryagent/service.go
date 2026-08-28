package primaryagent

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
)

type Store interface {
	LoadAgentRuntime(string) (*model.AgentRuntime, error)
	LoadTerminalRuntime(string) (*model.TerminalRuntime, error)
	SaveTerminalRuntime(context.Context, string, model.TerminalRuntime) error
	SaveAgentRuntime(context.Context, string, model.AgentRuntime) error
	SaveManifest(context.Context, model.Manifest) error
	AppendEvent(context.Context, string, model.Event) error
	ItemDir(string) string
}
type Process interface {
	Alive(int) bool
	Info(int) (model.ProcessInfo, error)
	TerminateGroup(int) error
	FindDescendant(int, []string) (model.ProcessInfo, bool, error)
}

type Git interface {
	Head(context.Context, string) (string, error)
	StatusPorcelain(context.Context, string) (string, error)
	CurrentBranch(context.Context, string) (string, error)
}

type Tmux interface {
	HasSession(context.Context, string) (bool, error)
	PaneInfo(context.Context, string) (model.TerminalPaneInfo, error)
}
type Resolver func(context.Context, contract.ResolveOptions) (model.Manifest, error)
type PiSessionObserver func(model.Manifest, time.Time) (PiSessionStatus, []string)

type Service struct {
	store             Store
	process           Process
	git               Git
	tmux              Tmux
	resolve           Resolver
	newID             func() (string, error)
	now               func() time.Time
	busy              func(context.Context, string) (bool, error)
	PiSessionObserver PiSessionObserver
	RuntimeStateRoot  string
	RuntimeSocketRoot string
}

func New(st Store, p Process, git Git, tmux Tmux, r Resolver, newID func() (string, error), now func() time.Time, busy func(context.Context, string) (bool, error)) *Service {
	return &Service{store: st, process: p, git: git, tmux: tmux, resolve: r, newID: newID, now: now, busy: busy}
}

// OwnershipStatus distinguishes verified process ownership from its live control channel.
type OwnershipStatus struct {
	RuntimeID        string `json:"runtime_id,omitempty"`
	Mode             string `json:"mode,omitempty"`
	HostPID          int    `json:"host_pid,omitempty"`
	ProcessAlive     bool   `json:"process_alive"`
	IdentityVerified bool   `json:"identity_verified"`
	ControlAvailable bool   `json:"control_available"`
}

type RuntimeStatusResult struct {
	WorkItemID   string              `json:"work_item_id"`
	Runtime      *model.AgentRuntime `json:"runtime,omitempty"`
	Ownership    OwnershipStatus     `json:"ownership"`
	Online       bool                `json:"online"`
	Capabilities agent.Capabilities  `json:"capabilities"`
	Warnings     []string            `json:"warnings"`
}

func (s *Service) ObserveOwnership(runtime *model.AgentRuntime) OwnershipStatus {
	if runtime == nil {
		return OwnershipStatus{}
	}
	status := OwnershipStatus{RuntimeID: runtime.ID, Mode: runtime.Mode, HostPID: runtime.HostPID}
	if strings.TrimSpace(runtime.ID) == "" || runtime.HostPID <= 0 || s.process == nil {
		return status
	}
	info, err := s.process.Info(runtime.HostPID)
	if err != nil || info.State == "Z" {
		return status
	}
	status.IdentityVerified = runtime.HostStartTime != 0 && runtime.HostProcessGroup != 0 && info.StartTime == runtime.HostStartTime && info.PGRP == runtime.HostProcessGroup
	if !status.IdentityVerified {
		return status
	}
	status.ProcessAlive = true
	if status.ProcessAlive && strings.TrimSpace(runtime.WorkItemID) != "" {
		_, err := os.Stat(s.ControlSocketPath(runtime.WorkItemID, runtime))
		status.ControlAvailable = err == nil
	}
	return status
}

func (s *Service) RuntimeStatus(ctx context.Context, opts contract.ResolveOptions) (RuntimeStatusResult, error) {
	m, e := s.resolve(ctx, opts)
	if e != nil {
		return RuntimeStatusResult{}, e
	}
	r, e := s.store.LoadAgentRuntime(m.ID)
	if e != nil {
		return RuntimeStatusResult{}, e
	}
	ownership := s.ObserveOwnership(r)
	res := RuntimeStatusResult{WorkItemID: m.ID, Runtime: r, Ownership: ownership, Online: ownership.ProcessAlive, Warnings: []string{}}
	if r == nil {
		return res, nil
	}
	mode, e := agent.ParseMode(r.Mode)
	if e != nil {
		res.Warnings = append(res.Warnings, e.Error())
	} else {
		res.Capabilities = agent.CapabilitiesForMode(mode)
	}
	return res, nil
}
