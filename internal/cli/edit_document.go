package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/regb/workitem/internal/coordinator"
)

// editDocument edits a private draft, never canonical content. Failed drafts
// remain available for recovery; successful callers remove the draft directory.
func editDocument(ctx context.Context, cfg Config, title, description string) (string, string, string, error) {
	directory, err := os.MkdirTemp("", "wi-edit-")
	if err != nil {
		return "", "", "", err
	}
	path := filepath.Join(directory, "item.md")
	if err := os.WriteFile(path, []byte("Title: "+title+"\n\n"+description), 0600); err != nil {
		os.RemoveAll(directory)
		return "", "", "", err
	}
	fail := func(err error) (string, string, string, error) {
		return "", "", path, fmt.Errorf("edit draft retained at %s: %w", path, err)
	}
	editor := strings.TrimSpace(cfg.Env["VISUAL"])
	if editor == "" {
		editor = strings.TrimSpace(cfg.Env["EDITOR"])
	}
	if editor == "" {
		editor = "vi"
	}
	// The command comes from trusted operator configuration; the filename remains
	// a separate argument, including when EDITOR contains flags or shell quoting.
	command := exec.CommandContext(ctx, "sh", "-c", editor+` "$@"`, "wi-edit", path)
	command.Dir = cfg.CWD
	command.Env = editEnvironment(cfg.Env)
	command.Stdin = cfg.Stdin
	command.Stdout = cfg.Stderr
	command.Stderr = cfg.Stderr
	if err := command.Run(); err != nil {
		return fail(fmt.Errorf("editor exited unsuccessfully: %w", err))
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fail(err)
	}
	if !info.Mode().IsRegular() || info.Size() > coordinator.MaxDescriptionBytes+1024 {
		return fail(fmt.Errorf("edited draft must be a regular file of at most 4 MiB plus its title"))
	}
	if err := os.Chmod(path, 0600); err != nil {
		return fail(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fail(err)
	}
	header, remainder, ok := strings.Cut(string(data), "\n")
	blank, body, separated := strings.Cut(remainder, "\n")
	if !ok || !separated || strings.TrimSpace(blank) != "" || !strings.HasPrefix(header, "Title: ") {
		return fail(fmt.Errorf("keep the first line 'Title: <text>', followed by a blank line and the description"))
	}
	editedTitle := strings.TrimSpace(strings.TrimPrefix(header, "Title: "))
	if err := coordinator.ValidateEditTitle(editedTitle); err != nil {
		return fail(err)
	}
	if err := coordinator.ValidateDescription(body); err != nil {
		return fail(err)
	}
	return editedTitle, body, path, nil
}
func editEnvironment(overrides map[string]string) []string {
	values := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}
