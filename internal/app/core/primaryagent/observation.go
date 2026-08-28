package primaryagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
)

const defaultAgentStaleAfter = 10 * time.Minute

var piProcessNeedles = []string{"pi", "pi-coding-agent", "@earendil-works/pi-coding-agent"}

type AgentStatusOptions struct {
	contract.ResolveOptions
	StaleAfter time.Duration
}

type AgentStatusResult struct {
	WorkItemID string                 `json:"work_item_id"`
	Status     string                 `json:"status"`
	Reason     string                 `json:"reason,omitempty"`
	ObservedAt time.Time              `json:"observed_at"`
	Terminal   *model.TerminalRuntime `json:"terminal,omitempty"`
	Runtime    *model.AgentRuntime    `json:"runtime,omitempty"`
	Process    AgentProcessStatus     `json:"process"`
	PiSession  PiSessionStatus        `json:"pi_session"`
	Worktree   *WorktreeStatus        `json:"worktree,omitempty"`
	Manifest   model.Manifest         `json:"manifest,omitempty"`
	Warnings   []string               `json:"warnings"`
}

type AgentProcessStatus struct {
	RuntimeID         string `json:"runtime_id,omitempty"`
	RuntimeMode       string `json:"runtime_mode,omitempty"`
	RuntimeHostPID    int    `json:"runtime_host_pid,omitempty"`
	ControlAvailable  bool   `json:"control_available"`
	TmuxSession       string `json:"tmux_session,omitempty"`
	TmuxWindow        string `json:"tmux_window,omitempty"`
	TmuxPaneID        string `json:"tmux_pane_id,omitempty"`
	TmuxPaneIndex     string `json:"tmux_pane_index,omitempty"`
	TmuxPanePID       int    `json:"tmux_pane_pid,omitempty"`
	TmuxPaneCommand   string `json:"tmux_pane_command,omitempty"`
	TmuxPanePath      string `json:"tmux_pane_path,omitempty"`
	TmuxPaneAlive     bool   `json:"tmux_pane_alive"`
	PiPID             int    `json:"pi_pid,omitempty"`
	PiProcessCommand  string `json:"pi_process_command,omitempty"`
	PiProcessAlive    bool   `json:"pi_process_alive"`
	Online            bool   `json:"online"`
	DiscoverySource   string `json:"discovery_source,omitempty"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

type PiSessionStatus struct {
	ID                     string                 `json:"id,omitempty"`
	Path                   string                 `json:"path,omitempty"`
	PathRel                string                 `json:"path_rel,omitempty"`
	Source                 string                 `json:"source,omitempty"`
	Exists                 bool                   `json:"exists"`
	SizeBytes              int64                  `json:"size_bytes,omitempty"`
	ModTime                time.Time              `json:"mod_time,omitempty"`
	EntriesScanned         int                    `json:"entries_scanned"`
	MalformedLines         int                    `json:"malformed_lines,omitempty"`
	LastEvent              *PiSessionEventSummary `json:"last_event,omitempty"`
	LastTurnActivity       *PiSessionEventSummary `json:"last_turn_activity,omitempty"`
	LastUserPrompt         *PiSessionEventSummary `json:"last_user_prompt,omitempty"`
	LastAssistantMessage   *PiSessionEventSummary `json:"last_assistant_message,omitempty"`
	LastTerminalAssistant  *PiSessionEventSummary `json:"last_terminal_assistant,omitempty"`
	LastToolActivity       *PiSessionEventSummary `json:"last_tool_activity,omitempty"`
	LastActivityAgeSeconds int64                  `json:"last_activity_age_seconds,omitempty"`
	InferredTurnState      string                 `json:"inferred_turn_state,omitempty"`
	UnavailableReason      string                 `json:"unavailable_reason,omitempty"`
}

type PiSessionEventSummary struct {
	Line         int       `json:"line"`
	Type         string    `json:"type"`
	Timestamp    time.Time `json:"timestamp,omitempty"`
	Role         string    `json:"role,omitempty"`
	StopReason   string    `json:"stop_reason,omitempty"`
	ContentTypes []string  `json:"content_types,omitempty"`
	Terminal     bool      `json:"terminal,omitempty"`
	Failed       bool      `json:"failed,omitempty"`
	TextPreview  string    `json:"text_preview,omitempty"`
}

type WorktreeStatus struct {
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
	CheckoutPath   string `json:"checkout_path,omitempty"`
	Head           string `json:"head,omitempty"`
	ExpectedBranch string `json:"expected_branch,omitempty"`
	CurrentBranch  string `json:"current_branch,omitempty"`
	BranchMatches  bool   `json:"branch_matches"`
	BranchMismatch bool   `json:"branch_mismatch,omitempty"`
	Dirty          bool   `json:"dirty"`
	HasChanges     bool   `json:"has_changes"`
}

func (s *Service) AbsPath(itemID, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("item path %q must be relative", rel)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("item path %q escapes the work-item directory", rel)
	}
	return filepath.Join(s.store.ItemDir(itemID), clean), nil
}

func (s *Service) AgentStatus(ctx context.Context, opts AgentStatusOptions) (AgentStatusResult, error) {
	m, err := s.resolve(ctx, opts.ResolveOptions)
	if err != nil {
		return AgentStatusResult{}, err
	}
	now := s.now()
	staleAfter := opts.StaleAfter
	if staleAfter <= 0 {
		staleAfter = defaultAgentStaleAfter
	}
	res := AgentStatusResult{WorkItemID: m.ID, ObservedAt: now, Manifest: m, Warnings: []string{}}
	if terminal, err := s.store.LoadTerminalRuntime(m.ID); err == nil {
		res.Terminal = terminal
	} else {
		res.Warnings = append(res.Warnings, "could not read terminal-runtime handle cache: "+err.Error())
	}
	if runtime, err := s.store.LoadAgentRuntime(m.ID); err == nil {
		res.Runtime = runtime
	} else {
		res.Warnings = append(res.Warnings, "could not read agent runtime: "+err.Error())
	}
	res.Process = s.inspectPrimaryAgentProcess(ctx, m, res.Terminal, res.Runtime)
	if res.Process.UnavailableReason != "" {
		res.Warnings = append(res.Warnings, res.Process.UnavailableReason)
	}
	var piSession PiSessionStatus
	var sessionWarnings []string
	if s.PiSessionObserver != nil {
		piSession, sessionWarnings = s.PiSessionObserver(m, now)
	} else {
		piSession, sessionWarnings = s.InspectPiSession(m, now)
	}
	res.PiSession = piSession
	res.Warnings = append(res.Warnings, sessionWarnings...)
	worktreeStatus, worktreeWarnings := s.inspectWorktree(ctx, m)
	res.Worktree = worktreeStatus
	res.Warnings = append(res.Warnings, worktreeWarnings...)
	if res.Process.Online && res.Process.RuntimeMode == string(agent.ModeTUI) && m.Checkout.Path != nil && strings.TrimSpace(res.Process.TmuxPanePath) != "" && !CheckoutContainsPath(*m.Checkout.Path, res.Process.TmuxPanePath) {
		res.Status = "problem"
		res.Reason = "active agent pane is outside the current work-item checkout"
		res.Warnings = append(res.Warnings, fmt.Sprintf("agent pane path %s does not belong to current checkout %s", res.Process.TmuxPanePath, *m.Checkout.Path))
		return res, nil
	}
	res.Status, res.Reason = deriveAgentStatus(res.Process, res.PiSession, now, staleAfter)
	return res, nil
}

func (s *Service) RecordPrimaryTerminalRuntime(ctx context.Context, m model.Manifest, window string) []string {
	warnings := []string{}
	terminal := model.TerminalRuntime{UpdatedAt: s.now(), TmuxWindow: window}
	if s.tmux != nil {
		pane, err := s.tmux.PaneInfo(ctx, m.TerminalSessionName()+":"+window)
		if err != nil {
			warnings = append(warnings, "could not inspect tmux pane after agent launch: "+err.Error())
		} else {
			applyPaneInfo(&terminal, pane)
		}
	}
	if err := s.store.SaveTerminalRuntime(ctx, m.ID, terminal); err != nil {
		warnings = append(warnings, "could not save terminal-runtime handle cache: "+err.Error())
	} else {
		_ = s.store.AppendEvent(ctx, m.ID, model.NewEvent(terminal.UpdatedAt, "terminal_runtime.recorded", "wi", map[string]any{"tmux_window": terminal.TmuxWindow, "tmux_pane_id": terminal.TmuxPaneID, "tmux_pane_pid": terminal.TmuxPanePID}))
	}
	return warnings
}

func (s *Service) inspectPrimaryAgentProcess(ctx context.Context, m model.Manifest, terminal *model.TerminalRuntime, runtime *model.AgentRuntime) AgentProcessStatus {
	status := AgentProcessStatus{TmuxSession: m.TerminalSessionName()}
	if runtime != nil {
		ownership := s.ObserveOwnership(runtime)
		status.RuntimeID = ownership.RuntimeID
		status.RuntimeMode = ownership.Mode
		status.RuntimeHostPID = ownership.HostPID
		status.ControlAvailable = ownership.ControlAvailable
		status.Online = ownership.ProcessAlive
		if status.Online {
			status.DiscoverySource = "agent-runtime"
			if proc, ok, err := s.process.FindDescendant(runtime.HostPID, piProcessNeedles); err == nil && ok {
				status.PiPID = proc.PID
				status.PiProcessCommand = proc.CommandLine()
				status.PiProcessAlive = s.process.Alive(proc.PID)
			}
		}
		if runtime.Mode == "rpc" {
			if !status.Online {
				status.UnavailableReason = "headless agent runtime is not online"
			}
			return status
		}
	}
	window := "agent"
	if terminal != nil {
		status.TmuxWindow = terminal.TmuxWindow
		status.TmuxPaneID = terminal.TmuxPaneID
		status.TmuxPaneIndex = terminal.TmuxPaneIndex
		status.TmuxPanePID = terminal.TmuxPanePID
		status.TmuxPaneCommand = terminal.TmuxPaneCommand
		status.TmuxPanePath = terminal.TmuxPanePath
		if terminal.TmuxWindow != "" {
			window = terminal.TmuxWindow
		}
	}
	if status.TmuxWindow == "" {
		status.TmuxWindow = window
	}
	paneObserved := false
	if s.tmux != nil {
		exists, err := s.tmux.HasSession(ctx, status.TmuxSession)
		if err != nil {
			status.UnavailableReason = "could not inspect primary tmux session: " + err.Error()
		} else if exists {
			pane, err := s.tmux.PaneInfo(ctx, status.TmuxSession+":"+window)
			if err != nil {
				status.UnavailableReason = "could not inspect primary tmux pane: " + err.Error()
			} else {
				applyPaneStatus(&status, pane)
				status.DiscoverySource = "tmux"
				paneObserved = true
			}
		}
	} else {
		status.UnavailableReason = "tmux adapter is not configured"
	}
	if s.process != nil {
		if paneObserved && status.TmuxPanePID > 0 {
			status.TmuxPaneAlive = s.process.Alive(status.TmuxPanePID)
		}
		if status.TmuxPaneAlive {
			if proc, ok, err := s.process.FindDescendant(status.TmuxPanePID, piProcessNeedles); err != nil {
				if status.UnavailableReason == "" {
					status.UnavailableReason = "could not discover Pi process: " + err.Error()
				}
			} else if ok {
				status.PiPID = proc.PID
				status.PiProcessCommand = proc.CommandLine()
				status.PiProcessAlive = s.process.Alive(proc.PID)
			}
		}
		if status.PiPID > 0 && !status.PiProcessAlive {
			status.PiProcessAlive = s.process.Alive(status.PiPID)
		}
	}
	status.Online = status.PiProcessAlive || (status.TmuxPaneAlive && status.PiPID == 0)
	return status
}

func (s *Service) InspectPiSession(m model.Manifest, now time.Time) (PiSessionStatus, []string) {
	warnings := []string{}
	status := PiSessionStatus{}
	if m.RootPiSession == nil {
		status.UnavailableReason = "no root Pi session is recorded for this work item"
		return status, warnings
	}
	status.ID = m.RootPiSession.ID
	status.PathRel = m.RootPiSession.Path
	status.Source = "manifest.root_pi_session"
	absPath, err := s.AbsPath(m.ID, m.RootPiSession.Path)
	if err != nil {
		status.UnavailableReason = err.Error()
		return status, warnings
	}
	status.Path = absPath
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			status.UnavailableReason = "Pi session file is missing"
			return status, warnings
		}
		status.UnavailableReason = "could not stat Pi session file: " + err.Error()
		return status, warnings
	}
	status.Exists = true
	status.SizeBytes = info.Size()
	status.ModTime = info.ModTime()
	file, err := os.Open(absPath)
	if err != nil {
		status.UnavailableReason = "could not open Pi session file: " + err.Error()
		return status, warnings
	}
	defer file.Close()
	parsed, parseWarnings := parsePiSessionJSONL(file)
	warnings = append(warnings, parseWarnings...)
	status.EntriesScanned = parsed.EntriesScanned
	status.MalformedLines = parsed.MalformedLines
	status.LastEvent = parsed.LastEvent
	status.LastTurnActivity = parsed.LastTurnActivity
	status.LastUserPrompt = parsed.LastUserPrompt
	status.LastAssistantMessage = parsed.LastAssistantMessage
	status.LastTerminalAssistant = parsed.LastTerminalAssistant
	status.LastToolActivity = parsed.LastToolActivity
	if status.LastTurnActivity != nil && !status.LastTurnActivity.Timestamp.IsZero() {
		status.LastActivityAgeSeconds = int64(now.Sub(status.LastTurnActivity.Timestamp).Seconds())
	}
	status.InferredTurnState = inferPiTurnState(status)
	return status, warnings
}

func (s *Service) inspectWorktree(ctx context.Context, m model.Manifest) (*WorktreeStatus, []string) {
	warnings := []string{}
	wt := &WorktreeStatus{Status: "absent", ExpectedBranch: expectedBranch(m), Reason: "checkout is absent"}
	if !m.Checkout.Present() || m.Checkout.Path == nil || *m.Checkout.Path == "" {
		return wt, warnings
	}
	wt.CheckoutPath = *m.Checkout.Path
	wt.Status = "problem"
	wt.Reason = ""
	if s.git == nil {
		wt.Reason = "git adapter is not configured"
		return wt, []string{wt.Reason}
	}
	if _, err := os.Stat(wt.CheckoutPath); err != nil {
		wt.Reason = "checkout path is unavailable: " + err.Error()
		return wt, []string{wt.Reason}
	}
	inspectionProblem := false
	branch, head, porcelain := "", "", ""
	if combined, ok := s.git.(interface {
		WorktreeSnapshot(context.Context, string) (string, string, string, error)
	}); ok {
		var err error
		head, branch, porcelain, err = combined.WorktreeSnapshot(ctx, wt.CheckoutPath)
		if err != nil {
			inspectionProblem = true
			wt.Reason = "could not inspect checkout: " + err.Error()
			warnings = append(warnings, wt.Reason)
		}
	} else {
		var branchErr, headErr, statusErr error
		branch, branchErr = s.git.CurrentBranch(ctx, wt.CheckoutPath)
		head, headErr = s.git.Head(ctx, wt.CheckoutPath)
		porcelain, statusErr = s.git.StatusPorcelain(ctx, wt.CheckoutPath)
		for _, failure := range []struct {
			label string
			err   error
		}{{"branch", branchErr}, {"HEAD", headErr}, {"status", statusErr}} {
			if failure.err != nil {
				inspectionProblem = true
				warning := fmt.Sprintf("could not read checkout %s: %s", failure.label, failure.err)
				warnings = append(warnings, warning)
				if wt.Reason == "" {
					wt.Reason = warning
				}
			}
		}
	}
	wt.CurrentBranch = branch
	wt.BranchMatches = branch == wt.ExpectedBranch
	wt.BranchMismatch = wt.ExpectedBranch != "" && branch != "" && branch != wt.ExpectedBranch
	if wt.BranchMismatch {
		wt.Reason = fmt.Sprintf("checkout branch %s differs from expected %s", branch, wt.ExpectedBranch)
		warnings = append(warnings, fmt.Sprintf("checkout branch mismatch: expected %s, found %s", wt.ExpectedBranch, branch))
	}
	wt.Head = head
	wt.Dirty = strings.TrimSpace(porcelain) != ""
	if wt.BranchMismatch || wt.CurrentBranch == "" || inspectionProblem {
		if wt.Reason == "" {
			wt.Reason = fmt.Sprintf("checkout branch %s differs from expected %s", wt.CurrentBranch, wt.ExpectedBranch)
		}
		wt.Status = "problem"
		return wt, warnings
	}
	if wt.Dirty {
		wt.Status = "changed"
		wt.HasChanges = true
		wt.Reason = "checkout has uncommitted changes"
		return wt, warnings
	}
	if wt.Head != "" && m.Repository.CreatedFromCommit != "" && wt.Head != m.Repository.CreatedFromCommit {
		wt.Status = "changed"
		wt.HasChanges = true
		wt.Reason = "checkout HEAD differs from the item's created-from commit"
		return wt, warnings
	}
	wt.Status = "clean"
	wt.Reason = "checkout matches the expected branch and created-from commit"
	return wt, warnings
}

func expectedBranch(m model.Manifest) string {
	return strings.TrimSpace(m.Checkout.Branch)
}

func deriveAgentStatus(process AgentProcessStatus, session PiSessionStatus, now time.Time, staleAfter time.Duration) (string, string) {
	if session.UnavailableReason != "" || !session.Exists {
		return "problem", session.UnavailableReason
	}
	if session.LastTurnActivity == nil {
		if !process.Online {
			return "problem", "Pi session has no turn activity and primary agent process is not online"
		}
		return "idle", "Pi session has no turn activity yet"
	}
	if session.InferredTurnState == "failed" && session.LastTerminalAssistant != nil {
		return "problem", "latest terminal assistant message ended with " + session.LastTerminalAssistant.StopReason
	}
	if session.InferredTurnState == "idle" {
		return "idle", "latest assistant answer is terminal"
	}
	age := time.Duration(0)
	if !session.LastTurnActivity.Timestamp.IsZero() {
		age = now.Sub(session.LastTurnActivity.Timestamp)
	}
	if !process.Online {
		return "problem", "latest Pi session activity is not terminal and primary agent process is not online"
	}
	if staleAfter > 0 && age > staleAfter {
		return "problem", fmt.Sprintf("latest Pi session activity is not terminal and is older than %s", staleAfter)
	}
	return "busy", "latest Pi session activity is not followed by a terminal assistant answer"
}

func piLineText(line rawPiSessionLine) string {
	content, err := line.contentBlocks()
	if err != nil {
		return ""
	}
	parts := []string{}
	for _, block := range content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, strings.TrimSpace(block.Text))
		}
	}
	return strings.Join(parts, "\n")
}

func inferPiTurnState(status PiSessionStatus) string {
	if status.LastTurnActivity == nil {
		return "idle"
	}
	if status.LastTerminalAssistant != nil {
		lastTerminal := status.LastTerminalAssistant.Timestamp
		if (status.LastUserPrompt == nil || !status.LastUserPrompt.Timestamp.After(lastTerminal)) && (status.LastToolActivity == nil || !status.LastToolActivity.Timestamp.After(lastTerminal)) {
			if status.LastTerminalAssistant.Failed {
				return "failed"
			}
			return "idle"
		}
	}
	return "incomplete"
}
