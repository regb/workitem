package workspacetool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
)

const ProtocolVersion = 1
const maxMessageBytes = 24 << 20

type Request struct {
	ProtocolVersion int               `json:"protocol_version"`
	Operation       string            `json:"operation,omitempty"`
	JobID           string            `json:"job_id,omitempty"`
	CWD             string            `json:"cwd,omitempty"`
	Command         []string          `json:"command"`
	Stdin           []byte            `json:"stdin,omitempty"`
	Environment     map[string]string `json:"environment,omitempty"`
	TimeoutMS       int64             `json:"timeout_ms,omitempty"`
}
type Response struct {
	ProtocolVersion int    `json:"protocol_version"`
	Done            bool   `json:"done,omitempty"`
	ExitCode        int    `json:"exit_code"`
	Stdout          []byte `json:"stdout,omitempty"`
	Stderr          []byte `json:"stderr,omitempty"`
	Truncated       bool   `json:"truncated,omitempty"`
	Error           string `json:"error,omitempty"`
}
type Handler func(context.Context, Request) (Response, error)
type StreamHandler func(context.Context, Request, func(Response) error) (Response, error)
type Server struct {
	listener net.Listener
	handler  StreamHandler
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

func Listen(path string, handler Handler) (*Server, error) {
	return ListenStream(path, func(ctx context.Context, r Request, _ func(Response) error) (Response, error) { return handler(ctx, r) })
}
func ListenStream(path string, handler StreamHandler) (*Server, error) {
	if handler == nil {
		return nil, fmt.Errorf("workspace tool handler is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	_ = os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{listener: listener, handler: handler, ctx: ctx, cancel: cancel}
	s.wg.Add(1)
	go s.serve()
	return s, nil
}
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.cancel()
	err := s.listener.Close()
	s.wg.Wait()
	return err
}
func (s *Server) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go s.handle(conn)
	}
}
func (s *Server) handle(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	go func() { <-ctx.Done(); conn.Close() }()
	var request Request
	if err := decode(conn, &request); err != nil {
		_ = encode(conn, Response{ProtocolVersion: ProtocolVersion, Done: true, Error: err.Error()})
		return
	}
	if request.ProtocolVersion != ProtocolVersion {
		_ = encode(conn, Response{ProtocolVersion: ProtocolVersion, Done: true, Error: "incompatible workspace tool protocol"})
		return
	}
	// EOF means this individual caller disappeared. This cancels an attached
	// operation, but job durability and explicit cancellation remain executor policy.
	go func() { var b [1]byte; _, _ = conn.Read(b[:]); cancel() }()
	emit := func(r Response) error { r.ProtocolVersion = ProtocolVersion; return encode(conn, r) }
	result, err := s.handler(ctx, request, emit)
	if err != nil {
		result.Error = err.Error()
	}
	result.Done = true
	_ = emit(result)
}
func Exchange(ctx context.Context, path string, request Request) (Response, error) {
	var result Response
	final, err := ExchangeStream(ctx, path, request, func(r Response) error {
		if len(result.Stdout)+len(r.Stdout) > 16<<20 || len(result.Stderr)+len(r.Stderr) > 16<<20 {
			return fmt.Errorf("workspace tool output limit exceeded")
		}
		result.Stdout = append(result.Stdout, r.Stdout...)
		result.Stderr = append(result.Stderr, r.Stderr...)
		return nil
	})
	result.ExitCode = final.ExitCode
	result.Error = final.Error
	result.Truncated = final.Truncated
	return result, err
}
func ExchangeStream(ctx context.Context, path string, request Request, emit func(Response) error) (Response, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	request.ProtocolVersion = ProtocolVersion
	if err := encode(conn, request); err != nil {
		return Response{}, err
	}
	reader := bufio.NewReader(conn)
	for {
		var response Response
		if err := decode(reader, &response); err != nil {
			return response, err
		}
		if response.ProtocolVersion != ProtocolVersion {
			return response, fmt.Errorf("incompatible workspace tool response")
		}
		if err := emit(response); err != nil {
			return response, err
		}
		if response.Done {
			if response.Error != "" {
				return response, fmt.Errorf("workspace tool: %s", response.Error)
			}
			return response, nil
		}
	}
}
func encode(w io.Writer, value any) error { return json.NewEncoder(w).Encode(value) }
func decode(r io.Reader, value any) error {
	// Frames are newline-delimited JSON. Reusing the buffered reader prevents a
	// decoder from consuming bytes belonging to the next streaming frame.
	reader, ok := r.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(r)
	}
	var data []byte
	for {
		chunk, prefix, err := reader.ReadLine()
		if err != nil {
			return err
		}
		if len(data)+len(chunk) > maxMessageBytes {
			return fmt.Errorf("workspace tool frame exceeds limit")
		}
		data = append(data, chunk...)
		if !prefix {
			break
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("unexpected trailing protocol data")
	}
	return nil
}
