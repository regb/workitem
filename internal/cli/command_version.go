package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/regb/workitem/internal/version"
)

func runVersion(args []string, cfg Config, jsonOut bool) error {
	if len(args) != 0 {
		return usageErr{errors.New("usage: wi version")}
	}
	info := version.Current()
	if jsonOut {
		return writeJSON(cfg.Stdout, info)
	}
	details := []string{}
	if info.Release != "" && info.ShortRevision != "" {
		details = append(details, info.ShortRevision)
	}
	if info.Modified {
		details = append(details, "dirty")
	}
	fmt.Fprintf(cfg.Stdout, "wi %s", info.Version)
	if len(details) > 0 {
		fmt.Fprintf(cfg.Stdout, " (%s)", strings.Join(details, ", "))
	}
	fmt.Fprintln(cfg.Stdout)
	return nil
}
