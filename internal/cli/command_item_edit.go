package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/coordinator"
	itemlock "github.com/regb/workitem/internal/lock"
	"github.com/regb/workitem/internal/model"
)

type editResult struct {
	WorkItemID         string         `json:"work_item_id"`
	Changed            bool           `json:"changed"`
	Title              string         `json:"title"`
	Labels             []string       `json:"labels"`
	DeepWork           bool           `json:"deep_work"`
	DescriptionChanged bool           `json:"description_changed"`
	Manifest           model.Manifest `json:"manifest"`
}

func runEdit(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("edit", cfg.Stderr)
	var item, title, descFile string
	var add, remove repeatFlag
	var deep, noDeep bool
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.StringVar(&title, "title", "", "replace the title without renaming the slug or branch")
	fs.StringVar(&descFile, "desc-file", "", "replace the description from a UTF-8 file")
	fs.Var(&add, "label", "add a label (repeatable)")
	fs.Var(&remove, "remove-label", "remove a label (repeatable)")
	fs.BoolVar(&deep, "deep", false, "mark as deep work")
	fs.BoolVar(&noDeep, "no-deep", false, "clear deep-work classification")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if fs.NArg() > 1 {
		return usageErr{fmt.Errorf("usage: wi edit [--item <selector>] [options] [item]")}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	titleSet, descSet, deepSet, noDeepSet := false, false, false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "title":
			titleSet = true
		case "desc-file":
			descSet = true
		case "deep":
			deepSet = true
		case "no-deep":
			noDeepSet = true
		}
	})
	if deepSet && noDeepSet {
		return usageErr{fmt.Errorf("--deep and --no-deep are mutually exclusive")}
	}
	edit := coordinator.MetadataEdit{AddLabels: add, RemoveLabels: remove}
	if titleSet {
		title = strings.TrimSpace(title)
		if err := coordinator.ValidateEditTitle(title); err != nil {
			return usageErr{err}
		}
		edit.Title = &title
	}
	if deepSet {
		edit.DeepWork = &deep
	}
	if noDeepSet {
		value := !noDeep
		edit.DeepWork = &value
	}
	if cfg.ControlAccess == coordinator.AccessAgent {
		return fmt.Errorf("agent endpoint does not allow item edits")
	}
	m, err := cfg.App.ResolveItem(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env})
	if err != nil {
		return err
	}
	operation, err := itemlock.AcquireOperation(ctx, cfg.App.Store.LockPath(m.ID))
	if err != nil {
		return err
	}
	locked := true
	defer func() {
		if locked {
			operation.Release()
		}
	}()
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	before, err := cfg.Coordinator.CanonicalManifest(requestCtx, m.ID)
	cancel()
	if err != nil {
		return err
	}
	var description *string
	var previousDescription string
	interactive := !titleSet && !descSet && !deepSet && !noDeepSet && len(add) == 0 && len(remove) == 0
	if descSet || interactive {
		previousDescription, err = coordinator.ReadEditDescription(cfg.App.Store, m.ID)
		if err != nil {
			return err
		}
	}
	draft := ""
	if interactive {
		operation.Release()
		locked = false
		editedTitle, editedDescription, path, err := editDocument(ctx, cfg, before.Manifest.Title, previousDescription)
		draft = path
		if err != nil {
			return err
		}
		if editedTitle != before.Manifest.Title {
			edit.Title = &editedTitle
		}
		description = &editedDescription
		operation, err = itemlock.AcquireOperation(ctx, cfg.App.Store.LockPath(m.ID))
		if err != nil {
			return fmt.Errorf("edit draft retained at %s: %w", draft, err)
		}
		locked = true
	} else if descSet {
		if strings.TrimSpace(descFile) == "" {
			return usageErr{fmt.Errorf("--desc-file requires a file path")}
		}
		path := descFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(cfg.CWD, path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("read --desc-file: %w", err)
		}
		if !info.Mode().IsRegular() || info.Size() > coordinator.MaxDescriptionBytes {
			return fmt.Errorf("--desc-file must be a regular file of at most 4 MiB")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		value := string(data)
		if err := coordinator.ValidateDescription(value); err != nil {
			return usageErr{err}
		}
		description = &value
	}
	if description != nil {
		edit.DescriptionDigest = coordinator.DescriptionDigest(*description)
		edit.PreviousDescriptionDigest = coordinator.DescriptionDigest(previousDescription)
	}
	id, err := model.NewID()
	if err != nil {
		return err
	}
	command := coordinator.ManifestCommand{ID: id, ProtocolVersion: coordinator.ProtocolVersion, Type: coordinator.CommandEdit, ItemID: m.ID, ExpectedRevision: &before.Revision, Actor: "user", Edit: &edit, CreatedAt: time.Now().UTC()}
	requestCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
	result, err := cfg.Coordinator.EditItem(requestCtx, coordinator.EditItemRequest{Command: command, Description: description})
	cancel()
	if err != nil {
		if draft != "" {
			return fmt.Errorf("edit draft retained at %s: %w", draft, err)
		}
		return err
	}
	if draft != "" {
		_ = os.RemoveAll(filepath.Dir(draft))
	}
	out := editResult{WorkItemID: result.Manifest.ID, Changed: result.Changed, Title: result.Manifest.Title, Labels: result.Manifest.Labels, DeepWork: result.Manifest.DeepWork, DescriptionChanged: description != nil && *description != previousDescription, Manifest: result.Manifest}
	if jsonOut {
		return writeJSON(cfg.Stdout, out)
	}
	if out.Changed {
		fmt.Fprintf(cfg.Stdout, "edited %s\n", out.WorkItemID)
	} else {
		fmt.Fprintf(cfg.Stdout, "unchanged: %s\n", out.WorkItemID)
	}
	return nil
}
