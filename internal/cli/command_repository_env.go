package cli

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

const repositoryEnvUsage = "usage: wi repo env <list|set|unset> [NAME] [--show-values] [KEY=VALUE...|KEY]"

// runRepositoryEnv manages a repository's environment variables, which Wi
// sets for the agents and setup commands it starts in its checkouts.
func runRepositoryEnv(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{fmt.Errorf(repositoryEnvUsage)}
	}
	action := args[0]
	fs := newFlagSet("repo env "+action, cfg.Stderr)
	showValues := false
	if action == "list" {
		fs.BoolVar(&showValues, "show-values", false, "print values, which may be secrets")
	}
	positional := []string{}
	for rest := args[1:]; ; {
		if err := fs.Parse(rest); err != nil {
			return usageErr{err}
		}
		if fs.NArg() == 0 {
			break
		}
		positional, rest = append(positional, fs.Arg(0)), fs.Args()[1:]
	}
	explicit := ""
	command := coordinator.SetRepositoryEnvironmentCommand{}
	switch action {
	case "list":
		if len(positional) > 1 {
			return usageErr{fmt.Errorf("usage: wi repo env list [NAME] [--show-values]")}
		}
		if len(positional) == 1 {
			explicit = positional[0]
		}
	case "set":
		// A first argument without "=" names the repository.
		if len(positional) > 0 && !strings.Contains(positional[0], "=") {
			explicit, positional = positional[0], positional[1:]
		}
		if len(positional) == 0 {
			return usageErr{fmt.Errorf("usage: wi repo env set [NAME] KEY=VALUE...")}
		}
		for _, arg := range positional {
			key, value, ok := strings.Cut(arg, "=")
			if !ok {
				return usageErr{fmt.Errorf("%q is not KEY=VALUE", arg)}
			}
			command.Set = append(command.Set, model.EnvVar{Key: key, Value: value})
		}
		if err := model.ValidEnv(command.Set); err != nil {
			return usageErr{err}
		}
	case "unset":
		switch len(positional) {
		case 1:
			command.Unset = positional
		case 2:
			explicit, command.Unset = positional[0], positional[1:]
		default:
			return usageErr{fmt.Errorf("usage: wi repo env unset [NAME] KEY")}
		}
	default:
		return usageErr{fmt.Errorf("unknown repository env action %q; %s", action, repositoryEnvUsage)}
	}
	selector, err := repositorySelector(ctx, cfg, "env "+action, explicit)
	if err != nil {
		return err
	}
	var result coordinator.RepositoryEnvironmentResult
	if action == "list" {
		result, err = cfg.Coordinator.RepositoryEnvironment(ctx, selector)
	} else {
		command.Selector = selector
		result, err = cfg.Coordinator.SetRepositoryEnvironment(ctx, command)
	}
	if err != nil {
		return err
	}
	if !showValues {
		for i := range result.Environment.Vars {
			result.Environment.Vars[i].Value = hiddenValue(result.Environment.Vars[i].Value)
		}
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, result)
	}
	if action != "list" {
		fmt.Fprintf(cfg.Stdout, "%s: environment updated; agents and setup started from now on get it\n", result.Repository.Name)
		return nil
	}
	if len(result.Environment.Vars) == 0 {
		fmt.Fprintln(cfg.Stdout, "no environment variables")
		return nil
	}
	writer := tabwriter.NewWriter(cfg.Stdout, 0, 4, 2, ' ', 0)
	for _, v := range result.Environment.Vars {
		fmt.Fprintf(writer, "%s\t%s\n", v.Key, v.Value)
	}
	return writer.Flush()
}

// hiddenValue stands for a value without showing it.
func hiddenValue(value string) string {
	if value == "" {
		return "(empty)"
	}
	return "(hidden)"
}
