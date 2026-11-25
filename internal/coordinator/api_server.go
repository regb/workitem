package coordinator

import (
	"context"
	"net/http"
	"path/filepath"
)

// InternalSocketPath is a daemon-private host endpoint for fenced runtime facts.
// Public clients use only wi.core.v1 on the daemon's API socket.
func InternalSocketPath(socket string) string {
	return portableSocketPath(filepath.Join(filepath.Dir(socket), "host-"+filepath.Base(socket)), "wi-host", filepath.Base(socket))
}

func (s *Server) SetBeforeClose(close func()) { s.beforeClose = close }

// ServeAPI serves the generated API on the daemon's socket.
// The private host endpoint is used only by daemon-owned runtime children.
func (s *Server) ServeAPI(ctx context.Context, handler http.Handler) error {
	path := InternalSocketPath(s.socketPath)
	if err := prepareSocketPath(path); err != nil {
		return err
	}
	listener, err := listenUnixSocket(path)
	if err != nil {
		return err
	}
	s.hostListener, s.apiHandler = listener, handler
	return s.Serve(ctx)
}
