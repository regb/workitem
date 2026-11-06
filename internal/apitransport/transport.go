// Package apitransport carries wi.core.v1 over authenticated local Unix sockets.
// It has no dependency on the daemon's storage or application use cases.
package apitransport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	"github.com/regb/workitem/gen/wi/core/v1/corev1connect"
	"github.com/regb/workitem/internal/socketpeer"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const baseURL = "http://wi.local"

// JSONCodec uses the contract's proto field names, including in CLI JSON.
// Registering it under "json" replaces Connect's default camelCase codec.
type JSONCodec struct{}

func (JSONCodec) Name() string { return "json" }

func (JSONCodec) Marshal(value any) ([]byte, error) {
	message, ok := value.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("not a protobuf message: %T", value)
	}
	return (protojson.MarshalOptions{UseProtoNames: true}).Marshal(message)
}

func (JSONCodec) Unmarshal(data []byte, value any) error {
	message, ok := value.(proto.Message)
	if !ok {
		return fmt.Errorf("not a protobuf message: %T", value)
	}
	return protojson.Unmarshal(data, message)
}

// Clients share one HTTP/2 connection pool. The caller owns Close. Streams
// have no global timeout; callers set deadlines or cancel their contexts.
type Clients struct {
	Items        corev1connect.ItemServiceClient
	Lifecycle    corev1connect.LifecycleServiceClient
	Workspaces   corev1connect.WorkspaceServiceClient
	Agents       corev1connect.AgentServiceClient
	Control      corev1connect.ControlServiceClient
	Repositories corev1connect.RepositoryServiceClient
	Profiles     corev1connect.AgentProfileServiceClient
	Jobs         corev1connect.JobServiceClient
	Events       corev1connect.EventServiceClient
	System       corev1connect.SystemServiceClient
	Views        corev1connect.ViewServiceClient
	transport    *http.Transport
}

// NewClients creates Connect JSON clients. The socket is never inferred from
// environment variables. Both peers must belong to this process's effective UID.
func NewClients(socketPath string) (*Clients, error) {
	if !filepath.IsAbs(socketPath) {
		return nil, errors.New("daemon socket path must be absolute")
	}
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	t := &http.Transport{
		Protocols: protocols,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			if err != nil {
				return nil, fmt.Errorf("connect to wi daemon: %w", err)
			}
			unixConn, ok := conn.(*net.UnixConn)
			if !ok {
				conn.Close()
				return nil, errors.New("daemon connection is not a Unix socket")
			}
			if err := socketpeer.Validate(unixConn); err != nil {
				conn.Close()
				return nil, err
			}
			return conn, nil
		},
	}
	client := &http.Client{
		Transport: t,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("daemon API redirects are not allowed")
		},
	}
	opts := []connect.ClientOption{connect.WithProtoJSON(), connect.WithCodec(JSONCodec{})}
	return &Clients{
		Items:        corev1connect.NewItemServiceClient(client, baseURL, opts...),
		Lifecycle:    corev1connect.NewLifecycleServiceClient(client, baseURL, opts...),
		Workspaces:   corev1connect.NewWorkspaceServiceClient(client, baseURL, opts...),
		Agents:       corev1connect.NewAgentServiceClient(client, baseURL, opts...),
		Control:      corev1connect.NewControlServiceClient(client, baseURL, opts...),
		Repositories: corev1connect.NewRepositoryServiceClient(client, baseURL, opts...),
		Profiles:     corev1connect.NewAgentProfileServiceClient(client, baseURL, opts...),
		Jobs:         corev1connect.NewJobServiceClient(client, baseURL, opts...),
		Events:       corev1connect.NewEventServiceClient(client, baseURL, opts...),
		System:       corev1connect.NewSystemServiceClient(client, baseURL, opts...),
		Views:        corev1connect.NewViewServiceClient(client, baseURL, opts...),
		transport:    t,
	}, nil
}

func (c *Clients) Close() { c.transport.CloseIdleConnections() }

// Access is assigned by the trusted listener, never by request metadata.
// Agent access is only an endpoint identity, not an item-scoped credential.
type Access string

const (
	Operator   Access = "operator"
	Agent      Access = "agent"
	WebAdapter Access = "web-adapter"
)

type accessKey struct{}

func Caller(ctx context.Context) Access {
	access, _ := ctx.Value(accessKey{}).(Access)
	return access
}

// Server serves authenticated peers. The caller owns socket creation and removal.
type Server struct{ http *http.Server }

// NewServer speaks Connect, gRPC and gRPC-Web on the same socket.
// No write timeout is installed because server streams can live indefinitely.
func NewServer(handler http.Handler, access Access) (*Server, error) {
	if access != Operator && access != Agent && access != WebAdapter {
		return nil, errors.New("invalid daemon endpoint identity")
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	return &Server{http: &http.Server{
		Handler:           handler,
		Protocols:         protocols,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
		ConnContext: func(ctx context.Context, _ net.Conn) context.Context {
			return context.WithValue(ctx, accessKey{}, access)
		},
	}}, nil
}

func (s *Server) Serve(listener *net.UnixListener) error {
	return s.http.Serve(peerListener{listener})
}

func (s *Server) Close() error { return s.http.Close() }

func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

// peerListener rejects foreign-UID peers before the HTTP parser sees bytes.
// Checking every accepted connection also covers reused HTTP/2 connections.
type peerListener struct{ *net.UnixListener }

func (l peerListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		if err := socketpeer.Validate(conn); err != nil {
			conn.Close()
			continue
		}
		return conn, nil
	}
}
