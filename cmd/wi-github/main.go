// Command wi-github is the optional GitHub plugin for wi. Enable it with
//
//	[plugins.github]
//	enabled = true
//
// and the wi daemon runs it every five minutes. It clears blockers that wait
// on GitHub pull requests once someone else posts on them or they are merged
// or closed. While it is enabled,
// `wi github wait <pull request>` creates such blockers.
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/regb/workitem/plugins/github"
)

func main() {
	wi := os.Getenv("WI_EXECUTABLE")
	if wi == "" {
		wi = "wi"
	}
	gh := os.Getenv("WI_GITHUB_GH")
	if gh == "" {
		gh = "gh"
	}
	settings, err := github.ParseSettings(os.Getenv("WI_PLUGIN_CONFIG"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "wi github:", err)
		os.Exit(1)
	}
	plugin := github.Plugin{Wi: wi, Gh: gh, Run: run, Out: os.Stdout, IgnoreAuthors: settings.IgnoreAuthors, RepositoryID: os.Getenv("WI_PLUGIN_REPOSITORY_ID")}
	args := os.Args[1:]
	// wi passes its global --json flag before the subcommand.
	jsonOut := len(args) > 0 && args[0] == "--json"
	if jsonOut {
		args = args[1:]
	}
	command := ""
	if len(args) > 0 {
		command, args = args[0], args[1:]
	}
	switch command {
	case "", "sync":
		err = plugin.Sync(context.Background())
	case "skill":
		fmt.Print(github.Skill)
	case "wait":
		if jsonOut {
			args = append(args, "--json")
		}
		err = plugin.Wait(context.Background(), args)
	default:
		fmt.Fprintln(os.Stderr, "usage: wi github wait <pull request URL | owner/repo#number> [--item <selector>]")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "wi github:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return nil, fmt.Errorf("%s: %s", err, message)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}
