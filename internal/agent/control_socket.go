package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrControlSocketUnavailable means no live runtime owner could be reached.
// Socket control is the only supported live command transport.
var ErrControlSocketUnavailable = errors.New("agent control socket unavailable")

type ControlSocketResponse struct {
	Accepted bool   `json:"accepted"`
	Error    string `json:"error,omitempty"`
}

type ControlSocketRequest struct {
	Command ControlCommand
	reply   chan error
}

func (r ControlSocketRequest) Respond(err error) { r.reply <- err }

type ControlSocketServer struct {
	listener net.Listener
	path     string
	requests chan ControlSocketRequest
	done     chan struct{}
	once     sync.Once
}

func ListenControlSocket(path string) (*ControlSocketServer, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("agent control socket path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		_ = os.Remove(path)
		return nil, err
	}
	s := &ControlSocketServer{listener: listener, path: path, requests: make(chan ControlSocketRequest), done: make(chan struct{})}
	go s.accept()
	return s, nil
}

func (s *ControlSocketServer) Requests() <-chan ControlSocketRequest { return s.requests }

func (s *ControlSocketServer) Close() error {
	var err error
	s.once.Do(func() {
		close(s.done)
		err = s.listener.Close()
		_ = os.Remove(s.path)
	})
	return err
}

func (s *ControlSocketServer) accept() {
	defer close(s.requests)
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
				continue
			}
		}
		go s.handle(conn)
	}
}

func (s *ControlSocketServer) handle(conn net.Conn) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	if !scanner.Scan() {
		return
	}
	var command ControlCommand
	if err := json.Unmarshal(scanner.Bytes(), &command); err != nil {
		_ = json.NewEncoder(conn).Encode(ControlSocketResponse{Error: "invalid control command: " + err.Error()})
		return
	}
	reply := make(chan error, 1)
	request := ControlSocketRequest{Command: command, reply: reply}
	select {
	case s.requests <- request:
	case <-s.done:
		return
	}
	select {
	case err := <-reply:
		response := ControlSocketResponse{Accepted: err == nil}
		if err != nil {
			response.Error = err.Error()
		}
		_ = json.NewEncoder(conn).Encode(response)
	case <-s.done:
	}
}

func SubmitControlSocket(ctx context.Context, path string, command ControlCommand) error {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrControlSocketUnavailable, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(command); err != nil {
		return err
	}
	var response ControlSocketResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return fmt.Errorf("agent control socket response: %w", err)
	}
	if !response.Accepted {
		if response.Error == "" {
			response.Error = "runtime rejected control command"
		}
		return errors.New(response.Error)
	}
	return nil
}
