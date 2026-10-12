package driverprotocol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"
)

// ContentVersion is independent of the process bootstrap and runtime protocols.
const ContentVersion = 1

const MaxContentTextBytes = 256 << 10

// Content is an ephemeral, read-only snapshot, never a retained runtime event.
// Text contains only the currently streaming assistant message. Final messages
// must be read from conversation.history. Readers replace, rather than append,
// snapshots, so reconnecting cannot duplicate deltas.
type Content struct {
	ProtocolVersion int          `json:"protocol_version"`
	RuntimeID       string       `json:"runtime_id"`
	WorkItemID      string       `json:"work_item_id"`
	Sequence        uint64       `json:"sequence"`
	IdleGuard       bool         `json:"idle_guard"`
	Text            string       `json:"text"`
	Truncated       bool         `json:"truncated,omitempty"`
	Activity        string       `json:"activity"`
	LeafID          string       `json:"leaf_id,omitempty"`
	Error           string       `json:"error,omitempty"`
	Tool            *ContentTool `json:"tool,omitempty"`
}
type ContentTool struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Input  string `json:"input,omitempty"`
	Output string `json:"output,omitempty"`
}
type ContentRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	RuntimeID       string `json:"runtime_id"`
	WorkItemID      string `json:"work_item_id"`
}

// ContentSocketPath is the optional v1 content endpoint for a runtime. Older
// drivers may not provide it. Runtime-specific paths avoid stale predecessor sockets.
// The filename has the same length as wi's bounded runtime control filenames.
func ContentSocketPath(control string) string {
	digest := sha256.Sum256([]byte("content:" + filepath.Base(control)))
	return filepath.Join(filepath.Dir(control), hex.EncodeToString(digest[:8])+".sock")
}

func ReadContent(ctx context.Context, path, runtimeID, itemID string) (Content, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return Content{}, err
	}
	defer conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if err := json.NewEncoder(conn).Encode(ContentRequest{ContentVersion, runtimeID, itemID}); err != nil {
		return Content{}, err
	}
	var value Content
	err = json.NewDecoder(io.LimitReader(conn, 2<<20)).Decode(&value)
	if err != nil {
		return value, err
	}
	if value.Error != "" {
		return value, fmt.Errorf("%s", value.Error)
	}
	if value.ProtocolVersion != ContentVersion || value.RuntimeID != runtimeID || value.WorkItemID != itemID {
		return Content{}, fmt.Errorf("content owner mismatch")
	}
	return value, nil
}

type ContentServer struct {
	listener net.Listener
	mu       sync.Mutex
	value    Content
	done     chan struct{}
}

func ListenContent(path, runtimeID, itemID string, idleGuard bool) (*ContentServer, error) {
	// Do not unlink an existing endpoint, which may belong to an active owner.
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = l.Close()
		return nil, err
	}
	s := &ContentServer{listener: l, value: Content{ProtocolVersion: ContentVersion, RuntimeID: runtimeID, WorkItemID: itemID, IdleGuard: idleGuard}, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s, nil
}
func (s *ContentServer) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	var req ContentRequest
	if json.NewDecoder(io.LimitReader(conn, 4096)).Decode(&req) != nil {
		return
	}
	s.mu.Lock()
	value := s.value
	s.mu.Unlock()
	if req.ProtocolVersion != ContentVersion || req.RuntimeID != value.RuntimeID || req.WorkItemID != value.WorkItemID {
		value = Content{Error: "content owner mismatch"}
	}
	_ = json.NewEncoder(conn).Encode(value)
}
func (s *ContentServer) Update(eventType, text, tool string) {
	s.UpdateTool(eventType, text, "", tool, "", "", false)
}
func (s *ContentServer) UpdateTool(eventType, text, toolID, toolName, input, output string, failed bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value.Sequence++
	switch eventType {
	case "message.started":
		s.value.Text = ""
		s.value.Tool = nil
		s.value.Truncated = false
		s.value.Activity = "Assistant responding"
	case EventMessageDelta:
		if s.value.Truncated {
			break
		}
		s.value.Text += text
		if len(s.value.Text) > MaxContentTextBytes {
			end := MaxContentTextBytes
			for end > 0 && !utf8.RuneStart(s.value.Text[end]) {
				end--
			}
			s.value.Text = s.value.Text[:end]
			s.value.Truncated = true
		}
		s.value.Activity = "Assistant responding"
	case EventMessageCompleted:
		s.value.Text = ""
		s.value.Truncated = false
		s.value.Activity = "Message saved"
	case EventToolStarted:
		s.value.Tool = &ContentTool{ID: toolID, Name: toolName, Status: "running", Input: boundedContentField(input)}
		s.value.Activity = "Tool running: " + toolName
	case EventToolUpdated:
		if s.value.Tool == nil || s.value.Tool.ID != toolID {
			s.value.Tool = &ContentTool{ID: toolID, Name: toolName, Status: "running"}
		}
		s.value.Tool.Output = boundedContentField(output)
		s.value.Activity = "Tool running: " + toolName
	case EventToolCompleted:
		status := "completed"
		if failed {
			status = "failed"
		}
		if s.value.Tool == nil || s.value.Tool.ID != toolID {
			s.value.Tool = &ContentTool{ID: toolID, Name: toolName}
		}
		s.value.Tool.Status = status
		s.value.Tool.Output = boundedContentField(output)
		s.value.Activity = "Tool " + status + ": " + toolName
	case EventAgentSettled:
		s.value.Text = ""
		s.value.Truncated = false
		s.value.Activity = "Idle"
	}
}
func boundedContentField(value string) string {
	if len(value) <= 64<<10 {
		return value
	}
	end := 64 << 10
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "\n[content truncated]"
}
func (s *ContentServer) Close() error { err := s.listener.Close(); <-s.done; return err }
