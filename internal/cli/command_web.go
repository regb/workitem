package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/regb/workitem/internal/coordinator"
	webaccess "github.com/regb/workitem/internal/web"
)

func runWeb(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("web", cfg.Stderr)
	var port int
	var noToken bool
	var accessURL, tokenFile string
	fs.IntVar(&port, "port", 0, "loopback port (default: choose an available port)")
	fs.StringVar(&accessURL, "access-url", "", "browser-facing HTTP(S) origin for a trusted reverse proxy")
	fs.BoolVar(&noToken, "no-token", false, "rely on the reverse proxy for access control instead of an access secret")
	fs.StringVar(&tokenFile, "token-file", "", "private file holding a persistent access secret; created when missing")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if fs.NArg() != 0 || port < 0 || port > 65535 || jsonOut {
		return usageErr{fmt.Errorf("usage: wi web [--port PORT] [--access-url ORIGIN] [--token-file PATH | --no-token]")}
	}
	if accessURL != "" && !strings.HasPrefix(accessURL, "https://") && !strings.HasPrefix(accessURL, "http://") {
		return usageErr{fmt.Errorf("--access-url requires an http:// or https:// origin")}
	}
	if noToken && (accessURL == "" || tokenFile != "") {
		return usageErr{fmt.Errorf("--no-token requires --access-url and excludes --token-file")}
	}
	if cfg.Coordinator == nil || cfg.ControlAccess != coordinator.AccessOperator {
		return fmt.Errorf("wi web requires the local operator daemon endpoint")
	}
	// Approvals go to the browser that asked; the server's terminal is not
	// watched.
	terminalApprovals.Store(false)
	options := webaccess.Options{AccessURL: accessURL, NoToken: noToken}
	if tokenFile != "" {
		token, err := loadOrCreateWebToken(tokenFile)
		if err != nil {
			return err
		}
		options.Token = token
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	defer listener.Close()
	if options.AccessURL == "" {
		options.AccessURL = "http://" + listener.Addr().String()
	}
	// Browser access is never the caller's current tmux pane.
	env := map[string]string{}
	for k, v := range cfg.Env {
		if k != "TMUX" && k != "TMUX_PANE" {
			env[k] = v
		}
	}
	cfg.Env = env
	handler, err := webaccess.New(webBackend{cfg}, options)
	if err != nil {
		if tokenFile != "" {
			return fmt.Errorf("%s: %w", tokenFile, err)
		}
		return err
	}
	defer handler.Close()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	switch {
	case noToken:
		fmt.Fprintf(cfg.Stdout, "Open this access URL:\n%s\nNo access secret: the proxy must restrict access. Ctrl-C stops serving, not agents.\n", handler.URL())
	case tokenFile != "":
		// A persistent secret stays in its file: a service's output may be
		// kept in a journal.
		path, _ := filepath.Abs(tokenFile)
		fmt.Fprintf(cfg.Stdout, "Serving %s\nThe access secret is in %s; open %s#token=SECRET with it. Stopping stops serving, not agents.\n", handler.Address(), path, handler.Address())
	default:
		fmt.Fprintf(cfg.Stdout, "Open this local access URL:\n%s\nKeep it private. Ctrl-C stops serving, not agents.\n", handler.URL())
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
			_ = server.Close()
		case <-done:
		}
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// loadOrCreateWebToken returns the secret stored in path, creating it with a
// fresh random secret when missing. The file must not be readable by others.
func loadOrCreateWebToken(path string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		token := hex.EncodeToString(b)
		_, err = f.WriteString(token + "\n")
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(path)
			return "", err
		}
		return token, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s must be a regular file accessible only by its owner", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
