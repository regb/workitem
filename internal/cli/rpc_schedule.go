package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// rpcSchedule runs `wi schedule`: list, add, edit, enable, disable, rm, run.
func rpcSchedule(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	action := "list"
	if len(args) > 0 {
		action, args = args[0], args[1:]
	}
	switch action {
	case "list", "ls":
		value, err := cfg.API.Schedules.ListSchedules(ctx, rpcRequest(cfg, &v1.ListSchedulesRequest{}))
		if err != nil {
			return err
		}
		if jsonOut {
			return rpcEmit(cfg, true, value.Msg)
		}
		return renderSchedules(cfg.Stdout, value.Msg.Schedules)
	case "add", "edit":
		return rpcPutSchedule(ctx, action, args, cfg, jsonOut)
	case "enable", "disable", "rm", "remove", "delete", "run":
	default:
		return usageErr{fmt.Errorf("unknown schedule action %q", action)}
	}
	fs := newFlagSet("schedule "+action, cfg.Stderr)
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usageErr{fmt.Errorf("schedule %s requires a schedule ID", action)}
	}
	current, err := findSchedule(ctx, cfg, fs.Arg(0))
	if err != nil {
		return err
	}
	switch action {
	case "enable", "disable":
		current.Enabled = action == "enable"
		value, err := cfg.API.Schedules.PutSchedule(ctx, rpcRequest(cfg, &v1.PutScheduleRequest{Schedule: current, ExpectedRevision: current.Revision}))
		if err != nil {
			return err
		}
		if jsonOut {
			return rpcEmit(cfg, true, value.Msg)
		}
		return renderSchedules(cfg.Stdout, []*v1.Schedule{value.Msg.Schedule})
	case "run":
		value, err := cfg.API.Schedules.RunSchedule(ctx, rpcRequest(cfg, &v1.RunScheduleRequest{Id: current.Id}))
		if err != nil {
			return err
		}
		if jsonOut {
			return rpcEmit(cfg, true, value.Msg)
		}
		_, err = fmt.Fprintf(cfg.Stdout, "started item %s\n", value.Msg.ItemId)
		return err
	}
	value, err := cfg.API.Schedules.DeleteSchedule(ctx, rpcRequest(cfg, &v1.DeleteScheduleRequest{Id: current.Id}))
	if err != nil {
		return err
	}
	if jsonOut {
		return rpcEmit(cfg, true, value.Msg)
	}
	_, err = fmt.Fprintf(cfg.Stdout, "removed schedule %s\n", current.Id)
	return err
}

func rpcPutSchedule(ctx context.Context, action string, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("schedule "+action, cfg.Stderr)
	repo := fs.String("repo", "", "repository name or ID")
	cron := fs.String("cron", "", "cron expression")
	title := fs.String("title", "", "item title; empty generates one")
	prompt := fs.String("prompt", "", "prompt text")
	promptFile := fs.String("prompt-file", "", "read the prompt from a file")
	stdin := fs.Bool("stdin", false, "read the prompt from standard input")
	agent := fs.String("agent", "", "agent profile")
	modelName := fs.String("model", "", "model for each run; empty uses the profile's")
	reasoning := fs.String("reasoning", "", "reasoning level for each run; empty uses the profile's")
	disabled := fs.Bool("disabled", false, "create the schedule disabled")
	var labels repeatFlag
	fs.Var(&labels, "label", "label; repeatable")
	clearLabels := fs.Bool("no-labels", false, "remove all labels")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	text, promptSet, err := schedulePrompt(cfg, *prompt, *promptFile, *stdin, set)
	if err != nil {
		return err
	}
	value := &v1.Schedule{Enabled: true}
	var revision uint32
	if action == "edit" {
		if fs.NArg() != 1 {
			return usageErr{errors.New("schedule edit requires a schedule ID")}
		}
		if value, err = findSchedule(ctx, cfg, fs.Arg(0)); err != nil {
			return err
		}
		revision = value.Revision
	} else {
		if fs.NArg() != 0 {
			return usageErr{errors.New("schedule add takes no positional arguments")}
		}
		for _, required := range []string{"repo", "cron", "agent"} {
			if !set[required] {
				return usageErr{fmt.Errorf("schedule add requires --%s", required)}
			}
		}
		if !promptSet {
			return usageErr{errors.New("schedule add requires --prompt, --prompt-file or --stdin")}
		}
	}
	if set["repo"] {
		value.RepositoryId = *repo
	}
	if set["cron"] {
		value.Cron = *cron
	}
	if set["title"] {
		value.Title = *title
	}
	if promptSet {
		value.Prompt = text
	}
	if set["agent"] {
		value.AgentProfile = *agent
	}
	// An empty value resets the setting to the profile's.
	for name, flagged := range map[string]*string{"model": modelName, "reasoning": reasoning} {
		if !set[name] {
			continue
		}
		if value.AgentSettings == nil {
			value.AgentSettings = map[string]string{}
		}
		if *flagged == "" {
			delete(value.AgentSettings, name)
		} else {
			value.AgentSettings[name] = *flagged
		}
	}
	if *clearLabels {
		value.Labels = nil
	}
	if len(labels) > 0 {
		value.Labels = append(value.Labels, labels...)
	}
	if set["disabled"] {
		value.Enabled = !*disabled
	}
	saved, err := cfg.API.Schedules.PutSchedule(ctx, rpcRequest(cfg, &v1.PutScheduleRequest{Schedule: value, ExpectedRevision: revision}))
	if err != nil {
		return err
	}
	if jsonOut {
		return rpcEmit(cfg, true, saved.Msg)
	}
	return renderSchedules(cfg.Stdout, []*v1.Schedule{saved.Msg.Schedule})
}

// schedulePrompt reads the prompt from at most one of --prompt,
// --prompt-file and --stdin.
func schedulePrompt(cfg Config, text, file string, stdin bool, set map[string]bool) (string, bool, error) {
	sources := 0
	for _, name := range []string{"prompt", "prompt-file", "stdin"} {
		if set[name] {
			sources++
		}
	}
	switch {
	case sources > 1:
		return "", false, usageErr{errors.New("pass only one of --prompt, --prompt-file and --stdin")}
	case set["prompt-file"]:
		data, err := os.ReadFile(file)
		return string(data), true, err
	case set["stdin"] && stdin:
		data, err := io.ReadAll(cfg.Stdin)
		return string(data), true, err
	case set["prompt"]:
		return text, true, nil
	}
	return "", false, nil
}

// findSchedule resolves a schedule ID or a unique prefix of one.
func findSchedule(ctx context.Context, cfg Config, selector string) (*v1.Schedule, error) {
	value, err := cfg.API.Schedules.ListSchedules(ctx, rpcRequest(cfg, &v1.ListSchedulesRequest{}))
	if err != nil {
		return nil, err
	}
	var found *v1.Schedule
	selector = strings.ToUpper(strings.TrimSpace(selector))
	for _, schedule := range value.Msg.Schedules {
		if schedule.Id == selector {
			return schedule, nil
		}
		if selector != "" && strings.HasPrefix(schedule.Id, selector) {
			if found != nil {
				return nil, usageErr{fmt.Errorf("schedule %q is ambiguous", selector)}
			}
			found = schedule
		}
	}
	if found == nil {
		return nil, fmt.Errorf("schedule %q not found", selector)
	}
	return found, nil
}

func renderSchedules(w io.Writer, schedules []*v1.Schedule) error {
	if len(schedules) == 0 {
		_, err := fmt.Fprintln(w, "no schedules")
		return err
	}
	for i, s := range schedules {
		if i > 0 {
			fmt.Fprintln(w)
		}
		state := "enabled"
		if !s.Enabled {
			state = "disabled"
		}
		fmt.Fprintf(w, "%s  %s  %s  %s\n", s.Id, s.Cron, state, s.RepositoryId)
		title := s.Title
		if title == "" {
			title = "(generated)"
		}
		agent := s.AgentProfile
		for _, name := range []string{"model", "reasoning"} {
			if value := s.AgentSettings[name]; value != "" {
				agent += ", " + name + " " + value
			}
		}
		fmt.Fprintf(w, "  Title:  %s\n  Agent:  %s\n", title, agent)
		if len(s.Labels) > 0 {
			fmt.Fprintf(w, "  Labels: %s\n", strings.Join(s.Labels, ", "))
		}
		prompt := strings.Join(strings.Fields(s.Prompt), " ")
		if len(prompt) > 100 {
			prompt = prompt[:97] + "..."
		}
		fmt.Fprintf(w, "  Prompt: %s\n", prompt)
		if s.Enabled {
			fmt.Fprintf(w, "  Next:   %s\n", humanTime(s.NextRunAt))
		}
		if s.LastRunAt != nil {
			last := humanTime(s.LastRunAt)
			if s.LastItemId != "" {
				last += "  item " + s.LastItemId
			}
			if s.LastError != "" {
				last += "  error: " + s.LastError
			}
			fmt.Fprintf(w, "  Last:   %s\n", last)
		}
	}
	return nil
}
