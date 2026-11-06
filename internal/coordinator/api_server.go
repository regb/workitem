package coordinator

import (
	"context"
	"net/http"
	"path/filepath"
)

// InternalSocketPath is a daemon-private host endpoint for fenced runtime facts.
// Public clients use only wi.core.v1 on the operator or restricted agent socket.
func InternalSocketPath(socket string) string {
	return portableSocketPath(filepath.Join(filepath.Dir(socket), "host-"+filepath.Base(socket)), "wi-host", filepath.Base(socket))
}

// WebSocketPath identifies the operator-authorized web-adapter listener.
func WebSocketPath(socket string) string {
	return portableSocketPath(filepath.Join(filepath.Dir(socket), "web-"+filepath.Base(socket)), "wi-web", filepath.Base(socket))
}

func (s *Server) SetBeforeClose(close func()) { s.beforeClose = close }

// ServeAPI replaces the operator and agent transports with the generated API.
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
	webPath := WebSocketPath(s.socketPath)
	if err := prepareSocketPath(webPath); err != nil {
		listener.Close()
		return err
	}
	web, err := listenUnixSocket(webPath)
	if err != nil {
		listener.Close()
		return err
	}
	s.webListener = web
	s.hostListener, s.apiHandler = listener, handler
	return s.Serve(ctx)
}
