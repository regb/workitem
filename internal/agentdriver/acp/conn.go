// Package acp is a client for the Agent Client Protocol: JSON-RPC 2.0, one
// message per line, between a client and an agent subprocess over its
// standard input and output. Both sides send requests; the agent asks the
// client for permissions, and notifies it of session updates.
package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// MaxMessageBytes bounds one protocol message.
const MaxMessageBytes = 16 << 20

// Message is one JSON-RPC message: a request (method and id), a
// notification (method only), or a response (id with result or error).
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC error.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if len(e.Data) > 0 && string(e.Data) != "null" {
		return fmt.Sprintf("%s (%d): %s", e.Message, e.Code, e.Data)
	}
	return fmt.Sprintf("%s (%d)", e.Message, e.Code)
}

// Incoming is a request or notification from the agent. A request carries
// an ID and must be answered with Conn.Respond.
type Incoming struct {
	ID     json.RawMessage
	Method string
	Params json.RawMessage
}

// IsRequest reports whether the agent expects a response.
func (m Incoming) IsRequest() bool { return len(m.ID) > 0 }

// Conn is a JSON-RPC connection to an agent. Responses to Call are matched
// by ID; everything else the agent sends arrives on Incoming, in order.
type Conn struct {
	writer   io.Writer
	writeMu  sync.Mutex
	mu       sync.Mutex
	next     int64
	pending  map[string]chan Message
	incoming chan Incoming
	done     chan struct{}
	err      error
	text     func(string)
}

// NewConn starts reading reader. Incoming messages queue up to buffer deep;
// a reader that falls further behind stops the connection rather than
// growing without bound.
// Plain-text lines go to text, when it is not nil.
func NewConn(reader io.Reader, writer io.Writer, buffer int, text func(string)) *Conn {
	c := &Conn{writer: writer, pending: map[string]chan Message{}, incoming: make(chan Incoming, buffer), done: make(chan struct{}), text: text}
	go c.read(reader)
	return c
}

// Incoming delivers the agent's requests and notifications. It is closed
// when the connection ends.
func (c *Conn) Incoming() <-chan Incoming { return c.incoming }

// Done is closed when the connection ends; Err then says why.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err is why the connection ended: io.EOF when the agent closed it.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Conn) read(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), MaxMessageBytes)
	var err error
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		// Some agents print plain text on standard output, such as a
		// sign-in link; only JSON objects are protocol messages.
		if line[0] != '{' {
			if c.text != nil {
				c.text(string(line))
			}
			continue
		}
		var m Message
		if err = json.Unmarshal(line, &m); err != nil {
			err = fmt.Errorf("malformed ACP message: %w", err)
			break
		}
		if m.Method == "" {
			c.mu.Lock()
			ch := c.pending[string(m.ID)]
			delete(c.pending, string(m.ID))
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
			continue
		}
		select {
		case c.incoming <- Incoming{ID: m.ID, Method: m.Method, Params: m.Params}:
		default:
			err = errors.New("ACP client fell behind the agent's messages")
		}
		if err != nil {
			break
		}
	}
	if err == nil {
		err = scanner.Err()
	}
	if err == nil {
		err = io.EOF
	}
	c.mu.Lock()
	c.err = err
	pending := c.pending
	c.pending = map[string]chan Message{}
	c.mu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
	close(c.incoming)
	close(c.done)
}

func (c *Conn) write(m Message) error {
	m.JSONRPC = "2.0"
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.writer.Write(append(data, '\n'))
	return err
}

// Call sends a request and waits for its response, decoding the result
// into result when it is not nil.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	ch, id, err := c.Send(method, params)
	if err != nil {
		return err
	}
	return c.Wait(ctx, ch, id, result)
}

// Send sends a request without waiting; Wait collects its response.
func (c *Conn) Send(method string, params any) (chan Message, string, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, "", err
	}
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return nil, "", fmt.Errorf("ACP connection closed: %w", err)
	}
	c.next++
	id := strconv.FormatInt(c.next, 10)
	ch := make(chan Message, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.write(Message{ID: json.RawMessage(id), Method: method, Params: raw}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, "", err
	}
	return ch, id, nil
}

// Wait waits for the response to a request sent with Send.
func (c *Conn) Wait(ctx context.Context, ch chan Message, id string, result any) error {
	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	case m, ok := <-ch:
		if !ok {
			return fmt.Errorf("ACP connection closed before a response: %w", c.Err())
		}
		if m.Error != nil {
			return m.Error
		}
		if result != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, result)
		}
		return nil
	}
}

// Notify sends a notification.
func (c *Conn) Notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.write(Message{Method: method, Params: raw})
}

// Respond answers a request from the agent with a result, or an error when
// err is not nil.
func (c *Conn) Respond(id json.RawMessage, result any, err *Error) error {
	if err != nil {
		return c.write(Message{ID: id, Error: err})
	}
	raw, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return marshalErr
	}
	return c.write(Message{ID: id, Result: raw})
}
