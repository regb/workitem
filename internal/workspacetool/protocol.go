package workspacetool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
)

const (
	ProtocolVersion = 1
	maxMessageBytes = 64 << 20
)

type Request struct {
	ProtocolVersion int               `json:"protocol_version"`
	CWD             string            `json:"cwd,omitempty"`
	Command         []string          `json:"command"`
	Stdin           []byte            `json:"stdin,omitempty"`
	Environment     map[string]string `json:"environment,omitempty"`
	TimeoutMS       int64             `json:"timeout_ms,omitempty"`
}

type Response struct {
	ProtocolVersion int    `json:"protocol_version"`
	ExitCode        int    `json:"exit_code"`
	Stdout          []byte `json:"stdout,omitempty"`
	Stderr          []byte `json:"stderr,omitempty"`
	Truncated       bool   `json:"truncated,omitempty"`
	Error           string `json:"error,omitempty"`
}

type Handler func(context.Context, Request) (Response, error)

type Server struct {
	listener net.Listener
	handler  Handler
}

func Listen(path string, handler Handler) (*Server, error) {
	if handler == nil {
		return nil, fmt.Errorf("workspace tool handler is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	server := &Server{listener: listener, handler: handler}
	go server.serve()
	return server, nil
}

func (s *Server) Close() error {
	if s == nil || s.listener == nil {
		return nil
	}
	return s.listener.Close()
}

func (s *Server) serve() {
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(connection)
	}
}

func (s *Server) handle(connection net.Conn) {
	defer connection.Close()
	var request Request
	if err := decode(connection, &request); err != nil {
		_ = encode(connection, Response{ProtocolVersion: ProtocolVersion, Error: err.Error()})
		return
	}
	if request.ProtocolVersion != ProtocolVersion {
		_ = encode(connection, Response{ProtocolVersion: ProtocolVersion, Error: fmt.Sprintf("workspace tool protocol %d is incompatible with %d", request.ProtocolVersion, ProtocolVersion)})
		return
	}
	response, err := s.handler(context.Background(), request)
	if err != nil {
		response.Error = err.Error()
	}
	response.ProtocolVersion = ProtocolVersion
	_ = encode(connection, response)
}

func Exchange(ctx context.Context, path string, request Request) (Response, error) {
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return Response{}, err
	}
	defer connection.Close()
	request.ProtocolVersion = ProtocolVersion
	if err := encode(connection, request); err != nil {
		return Response{}, err
	}
	var response Response
	ready := make(chan error, 1)
	go func() { ready <- decode(connection, &response) }()
	select {
	case <-ctx.Done():
		return Response{}, ctx.Err()
	case err := <-ready:
		if err != nil {
			return Response{}, err
		}
	}
	if response.ProtocolVersion != ProtocolVersion {
		return Response{}, fmt.Errorf("workspace tool response protocol %d is incompatible with %d", response.ProtocolVersion, ProtocolVersion)
	}
	if response.Error != "" {
		return response, fmt.Errorf("workspace tool: %s", response.Error)
	}
	return response, nil
}

func encode(writer io.Writer, value any) error {
	return json.NewEncoder(writer).Encode(value)
}

func decode(reader io.Reader, value any) error {
	limited := io.LimitReader(bufio.NewReader(reader), maxMessageBytes)
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}
