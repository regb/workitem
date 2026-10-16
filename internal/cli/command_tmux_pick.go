package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/rivo/uniseg"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
)

type pickerCandidate struct {
	Item    app.WorkListItem
	Section string
	Current bool
}

func runPicker(ctx context.Context, cfg Config, labels []string, noAgent, noPreview bool) error {
	selected, ok, err := pickItem(ctx, cfg, labels, noAgent, noPreview, false)
	if err != nil || !ok {
		return err
	}
	switch selected.Item.State {
	case model.StateBacklog:
		return startPickerItem(ctx, cfg, selected.Item.ID, "work_item.started")
	default:
		return switchSelectedItem(ctx, cfg, selected.Item.ID, false)
	}
}

// pickItem shows the interactive item picker and returns the selection
// without acting on it. excludeCurrent leaves out the caller's own item, for
// example when choosing what it should wait on.
func pickItem(ctx context.Context, cfg Config, labels []string, noAgent, noPreview, excludeCurrent bool) (pickerCandidate, bool, error) {
	if !noAgent {
		barrierCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
		barrier, err := cfg.Coordinator.ActivityBarrier(barrierCtx)
		cancel()
		if err != nil {
			return pickerCandidate{}, false, fmt.Errorf("refresh picker activity: %w", err)
		}
		for _, warning := range barrier.Projection.Warnings {
			fmt.Fprintf(cfg.Stderr, "warning: %s\n", warning)
		}
	}
	labelRules, err := effectiveListLabelRules(cfg.App.ListConfig.Labels, cfg.Env, labels)
	if err != nil {
		return pickerCandidate{}, false, usageErr{err}
	}
	res, err := daemonWorkList(ctx, cfg, app.WorkListOptions{LabelRules: labelRules})
	if err != nil {
		return pickerCandidate{}, false, fmt.Errorf("load picker candidates: %w", err)
	}
	agents := map[string]string(nil)
	if !noAgent {
		var warnings []string
		agents, warnings = enrichWorkListAgentStatus(ctx, cfg, &res)
		res.Warnings = append(res.Warnings, warnings...)
	}
	for _, warning := range res.Warnings {
		fmt.Fprintf(cfg.Stderr, "warning: %s\n", warning)
	}
	currentID := ""
	if current, err := cfg.App.ResolveItem(ctx, app.ResolveOptions{CWD: cfg.CWD, Env: cfg.Env}); err == nil {
		currentID = current.ID
	}
	candidates := pickerCandidates(res, agents, currentID)
	if excludeCurrent {
		kept := candidates[:0]
		for _, candidate := range candidates {
			if !candidate.Current {
				kept = append(kept, candidate)
			}
		}
		candidates = kept
	}
	if len(candidates) == 0 {
		return pickerCandidate{}, false, fmt.Errorf("no selectable work items")
	}
	return selectWithFZF(ctx, cfg, candidates, noPreview)
}

// runPick prints the ID of the item chosen in the picker, for scripts and
// tmux bindings that need an item without switching to it.
func runPick(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("pick", cfg.Stderr)
	var noAgent, noPreview, excludeCurrent bool
	var labels repeatFlag
	fs.BoolVar(&noAgent, "no-agent", false, "skip live agent/worktree inspection")
	fs.BoolVar(&noPreview, "no-preview", false, "disable the details preview")
	fs.BoolVar(&excludeCurrent, "exclude-current", false, "leave out the current item")
	fs.Var(&labels, "label", "include/exclude label rule; repeatable")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if fs.NArg() != 0 {
		return usageErr{errors.New("usage: wi pick [--exclude-current] [--no-agent] [--no-preview] [--label <rule>]...")}
	}
	selected, ok, err := pickItem(ctx, cfg, labels, noAgent, noPreview, excludeCurrent)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("no item selected")
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, map[string]string{"id": selected.Item.ID, "slug": selected.Item.Slug, "title": selected.Item.Title})
	}
	fmt.Fprintln(cfg.Stdout, selected.Item.ID)
	return nil
}

func startPickerItem(ctx context.Context, cfg Config, selector, eventType string) error {
	res, err := coordinatorStartWorkItem(ctx, cfg, selector, false, true, agent.ModeTUI, eventType)
	if err != nil {
		return err
	}
	printStartResult(cfg, res)
	return nil
}

func pickerCandidates(res app.WorkListResult, agents map[string]string, currentID string) []pickerCandidate {
	out := []pickerCandidate{}
	for _, section := range workDisplaySections(res, "active", agents) {
		for _, item := range section.Items {
			if item.State != model.StateActive && item.State != model.StateBacklog {
				continue
			}
			out = append(out, pickerCandidate{Item: item, Section: section.Name, Current: item.ID == currentID})
		}
	}
	return out
}

func selectWithFZF(ctx context.Context, cfg Config, candidates []pickerCandidate, noPreview bool) (pickerCandidate, bool, error) {
	byID := make(map[string]pickerCandidate, len(candidates))
	snapshot := pickerSnapshot(candidates)
	dir, err := os.MkdirTemp("", "wi-picker-")
	if err != nil {
		return pickerCandidate{}, false, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "snapshot.json")
	data, err := json.Marshal(snapshot)
	if err != nil {
		return pickerCandidate{}, false, err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return pickerCandidate{}, false, err
	}
	var input strings.Builder
	if err := snapshot.rows(&input, 78); err != nil {
		return pickerCandidate{}, false, err
	}
	for _, candidate := range candidates {
		byID[candidate.Item.ID] = candidate
	}
	self := "wi"
	if cfg.App != nil && strings.TrimSpace(cfg.App.SelfPath) != "" {
		self = cfg.App.SelfPath
	}
	render := shellQuotePicker(self) + " internal-picker-render " + shellQuotePicker(path) + " " + shellQuotePicker(self)
	bindings := pickerBindings(candidates) + ",start:unbind(result)+transform(" + render + "),resize:transform(" + render + ")"

	executable := strings.TrimSpace(cfg.Env["WI_FZF"])
	if executable == "" {
		executable = "fzf"
	}
	fzfArgs := []string{
		"--ansi",
		// A far-right match must not scroll each row by a different amount.
		"--no-hscroll",
		"--no-wrap",
		"--delimiter=\t",
		"--with-nth=2..",
		"--nth=2..",
		"--id-nth=1",
		"--scrollbar=",
		"--ellipsis=",
		"--prompt=wi> ",
		"--header=" + pickerDim + "FROZEN SNAPSHOT · reopen the picker to refresh" + pickerReset + "\n" + snapshot.header(78),
		"--header-first",
		"--height=100%",
		"--layout=reverse",
		"--cycle",
		"--border=none",
		"--padding=0",
		"--margin=0",
		"--pointer=▌",
		"--bind=" + bindings,
	}
	if !noPreview {
		fzfArgs = append(fzfArgs,
			"--preview="+pickerPreviewCommand(self),
			// The threshold is preview width, not terminal width. At 40%,
			// a preview narrower than 60 cells leaves too little list space.
			"--preview-window=right:40%:border-rounded:wrap:<60(down:40%:border-rounded:wrap)",
			"--bind=alt-p:toggle-preview+transform("+render+")",
			"--preview-label= details + status ",
		)
	}
	cmd := exec.CommandContext(ctx, executable, fzfArgs...)
	cmd.Stdin = strings.NewReader(input.String())
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = cfg.Stderr
	cmd.Env = pickerEnvironment(cfg.Env)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && (exitErr.ExitCode() == 1 || exitErr.ExitCode() == 130) {
			return pickerCandidate{}, false, nil
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return pickerCandidate{}, false, fmt.Errorf("fzf 0.71+ is required for interactive wi switch; install fzf or set WI_FZF to a compatible executable")
		}
		return pickerCandidate{}, false, fmt.Errorf("run work-item picker (requires fzf 0.71+): %w", err)
	}
	line := strings.TrimSpace(output.String())
	id, _, _ := strings.Cut(line, "\t")
	selected, ok := byID[id]
	if !ok {
		return pickerCandidate{}, false, fmt.Errorf("picker returned unknown work item %q", id)
	}
	return selected, true, nil
}

const (
	pickerReset   = "\x1b[0m"
	pickerBold    = "\x1b[1m"
	pickerDim     = "\x1b[2m"
	pickerRed     = "\x1b[31m"
	pickerGreen   = "\x1b[32m"
	pickerYellow  = "\x1b[33m"
	pickerBlue    = "\x1b[34m"
	pickerMagenta = "\x1b[35m"
	pickerCyan    = "\x1b[36m"
)

func pickerBindings(candidates []pickerCandidate) string {
	bindings := []string{"change:untrack-current+first+rebind(result)", "result:first+unbind(result)"}
	for index, candidate := range candidates {
		if candidate.Current {
			bindings = append(bindings, fmt.Sprintf("load:pos(%d)+unbind(load)", index+1))
			break
		}
	}
	return strings.Join(bindings, ",")
}

func pickerColumnWidths(candidates []pickerCandidate) (int, int) {
	statusWidth, repositoryWidth := len("STATUS"), len("REPOSITORY")
	for _, candidate := range candidates {
		label, _ := pickerSectionStyle(candidate.Section)
		statusWidth = max(statusWidth, uniseg.StringWidth(label))
		repositoryWidth = max(repositoryWidth, uniseg.StringWidth(pickerText(candidate.Item.Repository)))
	}
	return statusWidth, repositoryWidth
}

func pickerSectionBadge(section string, width int) string {
	label, color := pickerSectionStyle(section)
	return pickerBold + color + pickerDisplayCell(label, width) + pickerReset
}

func pickerSectionStyle(section string) (string, string) {
	label, color := pickerText(section), pickerBlue
	switch label {
	case "NEEDS ATTENTION":
		label, color = "ATTENTION", pickerYellow
	case "NEEDS FIXING":
		label, color = "FIXING", pickerRed
	case "IN PROGRESS":
		label, color = "ACTIVE", pickerGreen
	case "WAITING":
		label, color = "WAITING", pickerBlue
	case "BACKLOG":
		label, color = "BACKLOG", pickerCyan
	}
	return label, color
}

func pickerText(value string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value))
}

func pickerEnvironment(values map[string]string) []string {
	merged := environMap(os.Environ())
	for key, value := range values {
		merged[key] = value
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+merged[key])
	}
	return out
}

func pickerPreviewCommand(self string) string {
	executable := shellQuotePicker(self)
	return executable + " show --item {1}; printf '\\n--- observed status ---\\n'; " + executable + " agent status --item {1}"
}

func shellQuotePicker(value string) string {
	if value != "" && strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("_@%+=:,./-", r))
	}) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
