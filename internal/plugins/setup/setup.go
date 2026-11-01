// Package setup supplies the bundled plugin that runs a repository's setup
// command in each claimed checkout, after its files are copied and before the
// agent starts.
package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/regb/workitem/internal/config"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/options"
	"github.com/regb/workitem/internal/plugin"
	"github.com/regb/workitem/internal/plugins/files"
	"github.com/regb/workitem/internal/repofiles"
)

// Order runs setup after the files plugin copied the repository's files.
const Order = files.Order + 10

func Definition() options.Definition {
	min, max := int64(10), int64(7200)
	return options.Definition{Name: "setup", Options: []options.Option{
		{Name: "run", Type: "string", Default: "", Repository: true, Description: "Setup command run in each new checkout; replaces the one the repository declares."},
		{Name: "trust", Type: "string", Default: "never", Choices: []string{"never", "always"}, Repository: true, Description: "Whether to run the setup command declared in the repository's .config/wi.toml."},
		{Name: "timeout_seconds", Type: "integer", Default: int64(600), Min: &min, Max: &max, Repository: true, Description: "Time a setup command may take before it is stopped."},
	}}
}

func New() plugin.Hooks {
	return plugin.Hooks{Name: "setup", SetupCheckout: run, SetupOrder: Order}
}

func run(ctx context.Context, request plugin.SetupRequest, settings map[string]any) ([]string, error) {
	command, _ := settings["run"].(string)
	command = strings.TrimSpace(command)
	if command == "" {
		declared, err := DeclaredCommand(ctx, request.RepositoryRoot, request.DefaultBranch)
		if err != nil {
			return nil, err
		}
		if declared == "" {
			return nil, nil
		}
		if trust, _ := settings["trust"].(string); trust != "always" {
			return []string{"the repository declares a setup command in .config/wi.toml that was not run; run it with `wi repo settings set NAME plugins.setup.trust always`, or set your own with plugins.setup.run"}, nil
		}
		command = declared
	}
	staged, err := stageFiles(request.FilesDir)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staged)
	env := maps.Clone(request.Environment)
	env["WI_FILES"] = staged
	timeout := 600 * time.Second
	if seconds := integer(settings["timeout_seconds"]); seconds > 0 {
		timeout = time.Duration(seconds) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fmt.Fprintf(request.Log, "$ %s\n", command)
	if err := plugin.RunShell(runCtx, "setup", request.Subject.CheckoutPath, command, env, request.Log); err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("command did not finish within %s", timeout)
		}
		return nil, fmt.Errorf("command failed: %w", err)
	}
	return nil, nil
}

// DeclaredCommand reads the setup command a repository declares on its
// default branch, so a branch cannot change what runs.
func DeclaredCommand(ctx context.Context, root, branch string) (string, error) {
	if root == "" || branch == "" {
		return "", nil
	}
	content, err := gitpkg.New("git").ReadBlob(ctx, root, "refs/heads/"+branch, config.ProjectConfigRelativePath)
	if err != nil {
		// No project configuration on the default branch declares nothing.
		return "", nil
	}
	project, _, err := config.ReadProject(strings.NewReader(content))
	if err != nil {
		return "", fmt.Errorf("read %s on %s: %w", config.ProjectConfigRelativePath, branch, err)
	}
	return strings.TrimSpace(project.Workspace.Setup.Run), nil
}

// stageFiles copies the repository's files into a private directory that
// lives only for this run, so setup never sees wi's own storage.
func stageFiles(source string) (string, error) {
	dir, err := os.MkdirTemp("", "wi-files-")
	if err != nil {
		return "", err
	}
	stored, err := repofiles.List(source)
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("read repository files: %w", err)
	}
	for _, file := range stored {
		content, err := repofiles.Read(source, file.Path)
		if err == nil {
			_, err = repofiles.Put(dir, file.Path, content)
		}
		if err != nil {
			os.RemoveAll(dir)
			return "", err
		}
	}
	return filepath.Clean(dir), nil
}

func integer(value any) int64 {
	switch n := value.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	case json.Number:
		v, _ := n.Int64()
		return v
	}
	return 0
}
