package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/regb/workitem/internal/coordinator"
	webaccess "github.com/regb/workitem/internal/web"
)

func runWeb(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("web", cfg.Stderr)
	var port int
	var socketPath, accessURL, identityHeader string
	fs.IntVar(&port, "port", 0, "loopback port (default: choose an available port)")
	fs.StringVar(&socketPath, "socket", "", "serve on this Unix socket, reachable only by your user, instead of a port")
	fs.StringVar(&accessURL, "access-url", "", "browser-facing HTTP(S) origin served by a reverse proxy")
	fs.StringVar(&identityHeader, "identity-header", "", "header in which the proxy passes the authenticated user")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if fs.NArg() != 0 || port < 0 || port > 65535 || jsonOut {
		return usageErr{fmt.Errorf("usage: wi web [--port PORT | --socket PATH] [--access-url ORIGIN] [--identity-header NAME]")}
	}
	if accessURL != "" && !strings.HasPrefix(accessURL, "https://") && !strings.HasPrefix(accessURL, "http://") {
		return usageErr{fmt.Errorf("--access-url requires an http:// or https:// origin")}
	}
	if socketPath != "" && port != 0 {
		return usageErr{fmt.Errorf("--socket and --port are exclusive")}
	}
	if (socketPath != "" || identityHeader != "") && accessURL == "" {
		return usageErr{fmt.Errorf("--socket and --identity-header serve through a proxy: give its --access-url")}
	}
	if cfg.Coordinator == nil || cfg.ControlAccess != coordinator.AccessOperator {
		return fmt.Errorf("wi web requires the local operator daemon endpoint")
	}
	// A service manager stops the server with SIGTERM; shutting down cleanly
	// removes its socket.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Approvals go to the browser that asked; the server's terminal is not
	// watched.
	terminalApprovals.Store(false)
	var listener net.Listener
	var err error
	if socketPath != "" {
		listener, err = listenWebSocket(socketPath)
	} else {
		listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	}
	if err != nil {
		return err
	}
	defer listener.Close()
	options := webaccess.Options{AccessURL: accessURL, IdentityHeader: identityHeader}
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
		return err
	}
	defer handler.Close()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	switch {
	case accessURL == "":
		fmt.Fprintf(cfg.Stdout, "Serving %s\nThere is no sign-in: anything on this machine can use it. Ctrl-C stops serving, not agents.\n", handler.Address())
	case socketPath != "":
		fmt.Fprintf(cfg.Stdout, "Serving %s through the proxy on %s. Stopping stops serving, not agents.\n", handler.Address(), socketPath)
	default:
		fmt.Fprintf(cfg.Stdout, "Serving %s through the proxy on %s. Stopping stops serving, not agents.\n", handler.Address(), listener.Addr())
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

// listenWebSocket listens on a Unix socket only its owner can connect to,
// replacing a stale socket but never another file or a live server.
func listenWebSocket(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to replace %s: not a socket", path)
		}
		if conn, err := net.DialTimeout("unix", path, 100*time.Millisecond); err == nil {
			conn.Close()
			return nil, fmt.Errorf("something is already serving on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}
