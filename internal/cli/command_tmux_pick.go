package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode"

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
	if !noAgent {
		barrierCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
		barrier, err := cfg.Coordinator.ActivityBarrier(barrierCtx)
		cancel()
		if err != nil {
			return fmt.Errorf("refresh picker activity: %w", err)
		}
		for _, warning := range barrier.Projection.Warnings {
			fmt.Fprintf(cfg.Stderr, "warning: %s\n", warning)
		}
	}
	labelRules, err := effectiveListLabelRules(cfg.App.ListConfig.Labels, cfg.Env, labels)
	if err != nil {
		return usageErr{err}
	}
	res, err := daemonWorkList(ctx, cfg, app.WorkListOptions{LabelRules: labelRules})
	if err != nil {
		return fmt.Errorf("load picker candidates: %w", err)
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
	if len(candidates) == 0 {
		return fmt.Errorf("no selectable work items")
	}
	selected, ok, err := selectWithFZF(ctx, cfg, candidates, noPreview)
	if err != nil || !ok {
		return err
	}
	switch selected.Item.State {
	case model.StateWaiting:
		return startPickerItem(ctx, cfg, selected.Item.ID, "work_item.resumed")
	case model.StateBacklog:
		return startPickerItem(ctx, cfg, selected.Item.ID, "work_item.started")
	default:
		return switchSelectedItem(ctx, cfg, selected.Item.ID, false)
	}
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
			if item.State != model.StateWorking && item.State != model.StateWaiting && item.State != model.StateBacklog {
				continue
			}
			out = append(out, pickerCandidate{Item: item, Section: section.Name, Current: item.ID == currentID})
		}
	}
	return out
}

func selectWithFZF(ctx context.Context, cfg Config, candidates []pickerCandidate, noPreview bool) (pickerCandidate, bool, error) {
	byID := make(map[string]pickerCandidate, len(candidates))
	itemWidth, repositoryWidth := pickerColumnWidths(candidates)
	var input strings.Builder
	for _, candidate := range candidates {
		byID[candidate.Item.ID] = candidate
		fmt.Fprintf(&input, "%s\t%s\n", candidate.Item.ID, renderPickerCandidate(candidate, itemWidth, repositoryWidth))
	}

	executable := strings.TrimSpace(cfg.Env["WI_FZF"])
	if executable == "" {
		executable = "fzf"
	}
	fzfArgs := []string{
		"--ansi",
		"--delimiter=\t",
		"--with-nth=2",
		"--prompt=wi> ",
		"--header=" + renderPickerHeader(itemWidth, repositoryWidth),
		"--header-first",
		"--height=100%",
		"--layout=reverse",
		"--cycle",
		"--border=none",
		"--pointer=▌",
		"--bind=" + pickerBindings(candidates),
	}
	if !noPreview {
		self := strings.TrimSpace(cfg.App.SelfPath)
		if self == "" {
			self = "wi"
		}
		fzfArgs = append(fzfArgs,
			"--preview="+pickerPreviewCommand(self),
			"--preview-window=right:55%:wrap",
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
			return pickerCandidate{}, false, fmt.Errorf("fzf is required for interactive wi switch; install fzf or set WI_FZF to a compatible executable")
		}
		return pickerCandidate{}, false, fmt.Errorf("run work-item picker: %w", err)
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
	bindings := []string{"change:first"}
	for index, candidate := range candidates {
		if candidate.Current {
			bindings = append(bindings, fmt.Sprintf("load:pos(%d)", index+1))
			break
		}
	}
	return strings.Join(bindings, ",")
}

func pickerColumnWidths(candidates []pickerCandidate) (int, int) {
	itemWidth, repositoryWidth := len("ITEM"), len("REPOSITORY")
	for _, candidate := range candidates {
		if width := len([]rune(pickerText(candidate.Item.Slug))); width > itemWidth {
			itemWidth = width
		}
		if width := len([]rune(pickerText(candidate.Item.Repository))); width > repositoryWidth {
			repositoryWidth = width
		}
	}
	return max(18, min(itemWidth, 36)), max(18, min(repositoryWidth, 30))
}

func renderPickerCandidate(candidate pickerCandidate, itemWidth, repositoryWidth int) string {
	marker := "  "
	if candidate.Current {
		marker = pickerBold + pickerMagenta + "● " + pickerReset
	}
	title := pickerText(candidate.Item.Title)
	if title == pickerText(candidate.Item.Slug) {
		title = ""
	}
	return marker + pickerSectionBadge(candidate.Section) + "  " +
		pickerBold + pickerCell(candidate.Item.Slug, itemWidth) + pickerReset + "  " +
		pickerDim + pickerCell(candidate.Item.Repository, repositoryWidth) + pickerReset + "  " + title
}

func renderPickerHeader(itemWidth, repositoryWidth int) string {
	return pickerDim + "  FROZEN SNAPSHOT · reopen the picker to refresh" + pickerReset + "\n" +
		pickerBold + "  " + pickerCell("STATUS", 9) + "  " +
		pickerCell("ITEM", itemWidth) + "  " + pickerCell("REPOSITORY", repositoryWidth) + "  TITLE" + pickerReset
}

func pickerSectionBadge(section string) string {
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
	return pickerBold + color + pickerCell(label, 9) + pickerReset
}

func pickerCell(value string, width int) string {
	runes := []rune(pickerText(value))
	if len(runes) > width {
		if width <= 1 {
			return string(runes[:width])
		}
		runes = append(runes[:width-1], '…')
	}
	return string(runes) + strings.Repeat(" ", width-len(runes))
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
