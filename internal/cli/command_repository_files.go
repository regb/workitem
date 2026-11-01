package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/regb/workitem/internal/repofiles"
)

const repositoryFilesUsage = "usage: wi repo files <list|add|cat|remove> [NAME] [PATH] [--from FILE]"

// runRepositoryFiles manages a repository's files: untracked files such as
// a .env that wi copies into each of its checkouts.
func runRepositoryFiles(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{fmt.Errorf(repositoryFilesUsage)}
	}
	action := args[0]
	fs := newFlagSet("repo files "+action, cfg.Stderr)
	from := ""
	if action == "add" {
		fs.StringVar(&from, "from", "", "read the content from this file, or - for standard input (default: PATH)")
	}
	// Flags may follow the path: wi repo files add .env --from ~/x.env.
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
	explicit, path := "", ""
	switch action {
	case "list":
		if len(positional) > 1 {
			return usageErr{fmt.Errorf("usage: wi repo files list [NAME]")}
		}
		if len(positional) == 1 {
			explicit = positional[0]
		}
	case "add", "cat", "remove":
		switch len(positional) {
		case 1:
			path = positional[0]
		case 2:
			explicit, path = positional[0], positional[1]
		default:
			return usageErr{fmt.Errorf("usage: wi repo files %s [NAME] PATH", action)}
		}
		if err := repofiles.ValidPath(path); err != nil {
			return usageErr{err}
		}
	default:
		return usageErr{fmt.Errorf("unknown repository files action %q; %s", action, repositoryFilesUsage)}
	}
	selector, err := repositorySelector(ctx, cfg, "files "+action, explicit)
	if err != nil {
		return err
	}
	switch action {
	case "list":
		result, err := cfg.Coordinator.RepositoryFiles(ctx, selector)
		if err != nil {
			return err
		}
		if jsonOut {
			return writeJSON(cfg.Stdout, result)
		}
		if len(result.Files) == 0 {
			fmt.Fprintln(cfg.Stdout, "no repository files")
			return nil
		}
		writer := tabwriter.NewWriter(cfg.Stdout, 0, 4, 2, ' ', 0)
		for _, file := range result.Files {
			fmt.Fprintf(writer, "%s\t%d bytes\t%s\n", file.Path, file.Size, file.UpdatedAt.Local().Format("2006-01-02 15:04"))
		}
		return writer.Flush()
	case "add":
		content, err := readFileSource(cfg, from, path)
		if err != nil {
			return err
		}
		file, err := cfg.Coordinator.PutRepositoryFile(ctx, selector, path, content)
		if err != nil {
			return err
		}
		if err := syncRepositoryFiles(ctx, cfg, selector); err != nil {
			return err
		}
		if jsonOut {
			return writeJSON(cfg.Stdout, file)
		}
		fmt.Fprintf(cfg.Stdout, "stored %s (%d bytes); it is copied into each new checkout\n", file.Path, file.Size)
		return nil
	case "cat":
		result, err := cfg.Coordinator.ReadRepositoryFile(ctx, selector, path)
		if err != nil {
			return err
		}
		_, err = cfg.Stdout.Write(result.Content)
		return err
	default:
		if err := cfg.Coordinator.RemoveRepositoryFile(ctx, selector, path); err != nil {
			return err
		}
		if err := syncRepositoryFiles(ctx, cfg, selector); err != nil {
			return err
		}
		if jsonOut {
			return writeJSON(cfg.Stdout, map[string]string{"removed": path})
		}
		fmt.Fprintf(cfg.Stdout, "removed %s; checkouts not in use lost their unedited copy, the others lose it at their next claim\n", path)
		return nil
	}
}

func readFileSource(cfg Config, from, path string) ([]byte, error) {
	if from == "-" {
		return io.ReadAll(io.LimitReader(cfg.Stdin, repofiles.MaxBytes+1))
	}
	if from == "" {
		from = path
	}
	if !filepath.IsAbs(from) {
		from = filepath.Join(cfg.CWD, from)
	}
	info, err := os.Stat(from)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", from)
	}
	if info.Size() > repofiles.MaxBytes {
		return nil, fmt.Errorf("%s is %d bytes; the limit is %d", from, info.Size(), repofiles.MaxBytes)
	}
	return os.ReadFile(from)
}

// syncRepositoryFiles updates the repository's checkouts and info/exclude
// after a change, reporting copies it left because they were edited.
func syncRepositoryFiles(ctx context.Context, cfg Config, selector string) error {
	listed, err := cfg.Coordinator.RepositoryFiles(ctx, selector)
	if err != nil {
		return err
	}
	warnings, err := cfg.App.SyncRepositoryFiles(ctx, listed.RepositoryID)
	for _, warning := range warnings {
		fmt.Fprintf(cfg.Stderr, "warning: %s\n", warning)
	}
	if err != nil {
		return fmt.Errorf("the repository files changed, but updating its checkouts failed: %w", err)
	}
	return nil
}
