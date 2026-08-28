package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func runAgent(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{errors.New("usage: wi agent <status|control|runtime|monitor> [options]")}
	}
	switch args[0] {
	case "status":
		return runAgentStatus(ctx, args[1:], cfg, jsonOut)
	case "control":
		return runAgentControl(ctx, args[1:], cfg, jsonOut)
	case "runtime":
		return runAgentRuntime(ctx, args[1:], cfg, jsonOut)
	case "monitor":
		return runAgentMonitor(ctx, args[1:], cfg, jsonOut)
	case "exec":
		return runAgentExec(ctx, args[1:], cfg)
	default:
		return usageErr{fmt.Errorf("unknown agent subcommand %q", args[0])}
	}
}

func readAgentControlMessage(cfg Config, args []string, filePath string, fromStdin bool) (string, error) {
	sources := 0
	if strings.TrimSpace(filePath) != "" {
		sources++
	}
	if fromStdin {
		sources++
	}
	if len(args) > 0 {
		sources++
	}
	if sources == 0 {
		return "", errors.New("pass a message with --stdin, --file, or positional text")
	}
	if sources > 1 {
		return "", errors.New("pass only one message source: --stdin, --file, or positional text")
	}
	var message string
	if fromStdin {
		value, err := io.ReadAll(cfg.Stdin)
		if err != nil {
			return "", err
		}
		message = string(value)
	} else if strings.TrimSpace(filePath) != "" {
		path := strings.TrimSpace(filePath)
		if !filepath.IsAbs(path) && cfg.CWD != "" {
			path = filepath.Join(cfg.CWD, path)
		}
		value, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		message = string(value)
	} else {
		message = strings.Join(args, " ")
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return "", errors.New("agent control message is empty")
	}
	return message, nil
}
