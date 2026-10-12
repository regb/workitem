package driverprotocol

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

type ControlRejectedError struct{ Message string }

func (e *ControlRejectedError) Error() string { return e.Message }

type ControlResponse struct {
	Accepted bool   `json:"accepted"`
	Error    string `json:"error,omitempty"`
}

type socketReply struct {
	err  error
	sent chan struct{}
}

type ControlRequest struct {
	Command ControlCommand
	reply   chan socketReply
	closed  <-chan struct{}
}

func (r ControlRequest) Respond(err error) {
	response := socketReply{err: err, sent: make(chan struct{})}
	select {
	case r.reply <- response:
	case <-r.closed:
		return
	}
	select {
	case <-response.sent:
	case <-r.closed:
	}
}

type ControlServer struct {
	listener    net.Listener
	path        string
	requests    chan ControlRequest
	done        chan struct{}
	closed      chan struct{}
	once        sync.Once
	handlers    sync.WaitGroup
	connections map[net.Conn]struct{}
	mu          sync.Mutex
}

func ListenControl(path string) (*ControlServer, error) {
	listener, err := listenRuntimeSocket(path)
	if err != nil {
		return nil, err
	}
	s := &ControlServer{listener: listener, path: path, requests: make(chan ControlRequest), done: make(chan struct{}), closed: make(chan struct{}), connections: map[net.Conn]struct{}{}}
	go s.accept()
	return s, nil
}

func (s *ControlServer) Requests() <-chan ControlRequest { return s.requests }
func (s *ControlServer) Close() error {
	var err error
	s.once.Do(func() {
		close(s.done)
		err = s.listener.Close()
		s.closeConnections()
		<-s.closed
		_ = os.Remove(s.path)
	})
	return err
}
func (s *ControlServer) accept() {
	defer func() { s.handlers.Wait(); close(s.requests); close(s.closed) }()
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
		s.mu.Lock()
		// Accept may finish after Close has already swept the connections.
		select {
		case <-s.done:
			s.mu.Unlock()
			_ = conn.Close()
			return
		default:
			s.connections[conn] = struct{}{}
		}
		s.mu.Unlock()
		s.handlers.Add(1)
		go func() {
			defer s.handlers.Done()
			defer func() { s.mu.Lock(); delete(s.connections, conn); s.mu.Unlock(); _ = conn.Close() }()
			var command ControlCommand
			if err := decodeLine(conn, &command, MaxControlCommandBytes); err != nil {
				_ = json.NewEncoder(conn).Encode(ControlResponse{Error: "invalid control command: " + err.Error()})
				return
			}
			if err := command.ValidateContent(); err != nil {
				_ = json.NewEncoder(conn).Encode(ControlResponse{Error: err.Error()})
				return
			}
			reply := make(chan socketReply, 1)
			select {
			case s.requests <- ControlRequest{Command: command, reply: reply, closed: s.done}:
			case <-s.done:
				return
			}
			select {
			case result := <-reply:
				response := ControlResponse{Accepted: result.err == nil}
				if result.err != nil {
					response.Error = result.err.Error()
				}
				_ = json.NewEncoder(conn).Encode(response)
				close(result.sent)
			case <-s.done:
			}
		}()
	}
}

func (s *ControlServer) closeConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for conn := range s.connections {
		_ = conn.Close()
	}
}

func SubmitControl(ctx context.Context, path string, command ControlCommand) error {
	if err := command.ValidateContent(); err != nil {
		return err
	}
	encoded, err := json.Marshal(command)
	if err != nil {
		return err
	}
	if len(encoded) > MaxControlCommandBytes {
		return fmt.Errorf("control command exceeds 12 MiB")
	}
	var response ControlResponse
	if err := exchangeRuntimeSocket(ctx, path, command, &response); err != nil {
		return err
	}
	if !response.Accepted {
		if response.Error == "" {
			response.Error = "runtime rejected control command"
		}
		return &ControlRejectedError{Message: response.Error}
	}
	return nil
}

type EventRejectedError struct{ Message string }

func (e *EventRejectedError) Error() string { return e.Message }

type EventResponse struct {
	Accepted bool   `json:"accepted"`
	Error    string `json:"error,omitempty"`
}

type EventRequest struct {
	Event  RuntimeEvent
	reply  chan socketReply
	closed <-chan struct{}
}

func (r EventRequest) Respond(err error) {
	response := socketReply{err: err, sent: make(chan struct{})}
	select {
	case r.reply <- response:
	case <-r.closed:
		return
	}
	select {
	case <-response.sent:
	case <-r.closed:
	}
}

type EventServer struct {
	listener    net.Listener
	path        string
	requests    chan EventRequest
	done        chan struct{}
	closed      chan struct{}
	once        sync.Once
	handlers    sync.WaitGroup
	connections map[net.Conn]struct{}
	mu          sync.Mutex
}

func ListenEvents(path string) (*EventServer, error) {
	listener, err := listenRuntimeSocket(path)
	if err != nil {
		return nil, err
	}
	s := &EventServer{listener: listener, path: path, requests: make(chan EventRequest), done: make(chan struct{}), closed: make(chan struct{}), connections: map[net.Conn]struct{}{}}
	go s.accept()
	return s, nil
}
func (s *EventServer) Requests() <-chan EventRequest { return s.requests }
func (s *EventServer) Close() error {
	var err error
	s.once.Do(func() {
		close(s.done)
		err = s.listener.Close()
		s.closeConnections()
		<-s.closed
		_ = os.Remove(s.path)
	})
	return err
}
func (s *EventServer) accept() {
	defer func() { s.handlers.Wait(); close(s.requests); close(s.closed) }()
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
		s.mu.Lock()
		// Accept may finish after Close has already swept the connections.
		select {
		case <-s.done:
			s.mu.Unlock()
			_ = conn.Close()
			return
		default:
			s.connections[conn] = struct{}{}
		}
		s.mu.Unlock()
		s.handlers.Add(1)
		go func() {
			defer s.handlers.Done()
			defer func() { s.mu.Lock(); delete(s.connections, conn); s.mu.Unlock(); _ = conn.Close() }()
			var event RuntimeEvent
			if err := decodeLine(conn, &event, 1<<20); err != nil {
				_ = json.NewEncoder(conn).Encode(EventResponse{Error: "invalid runtime event: " + err.Error()})
				return
			}
			reply := make(chan socketReply, 1)
			select {
			case s.requests <- EventRequest{Event: event, reply: reply, closed: s.done}:
			case <-s.done:
				return
			}
			select {
			case result := <-reply:
				response := EventResponse{Accepted: result.err == nil}
				if result.err != nil {
					response.Error = result.err.Error()
				}
				_ = json.NewEncoder(conn).Encode(response)
				close(result.sent)
			case <-s.done:
			}
		}()
	}
}

func (s *EventServer) closeConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for conn := range s.connections {
		_ = conn.Close()
	}
}

func SubmitEvent(ctx context.Context, path string, event RuntimeEvent) error {
	var response EventResponse
	if err := exchangeRuntimeSocket(ctx, path, event, &response); err != nil {
		return err
	}
	if !response.Accepted {
		if response.Error == "" {
			response.Error = "runtime event was rejected"
		}
		return &EventRejectedError{Message: response.Error}
	}
	return nil
}

func listenRuntimeSocket(path string) (net.Listener, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("runtime socket path is required")
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
	return listener, nil
}

func exchangeRuntimeSocket(ctx context.Context, path string, request, response any) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return err
	}
	return json.NewDecoder(conn).Decode(response)
}

func decodeLine(conn net.Conn, target any, limit int) error {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), limit+2)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return err
		}
		return errors.New("empty request")
	}
	if len(scanner.Bytes()) > limit {
		return fmt.Errorf("socket request exceeds %d bytes", limit)
	}
	return json.Unmarshal(scanner.Bytes(), target)
}
