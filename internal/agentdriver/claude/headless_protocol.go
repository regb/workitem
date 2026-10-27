package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

const maxHeadlessFrame = 16 << 20

// These objects never go to the compact event socket. In particular init
// responses include account information and frames include unrestricted text.
type headlessFrame struct {
	Type           string   `json:"type"`
	Subtype        string   `json:"subtype"`
	SessionID      string   `json:"session_id"`
	UUID           string   `json:"uuid"`
	ParentToolID   *string  `json:"parent_tool_use_id"`
	UserID         string   `json:"user_message_uuid"`
	UserIDs        []string `json:"user_message_uuids"`
	CommandUUID    string   `json:"command_uuid"`
	State          string   `json:"state"`
	IsReplay       bool     `json:"isReplay"`
	IsError        bool     `json:"is_error"`
	Error          string   `json:"error"`
	APIErrorStatus int      `json:"api_error_status"`
	Result         string   `json:"result"`
	TerminalReason string   `json:"terminal_reason"`
	StopReason     string   `json:"stop_reason"`
	NumTurns       int      `json:"num_turns"`
	Capabilities   []string `json:"capabilities"`
	Status         string   `json:"status"`
	// Model is the session's model, on a system init frame.
	Model   string          `json:"model"`
	Event   json.RawMessage `json:"event"`
	Message struct {
		ID      string          `json:"id"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	RequestID string `json:"request_id"`
	Request   struct {
		Subtype string `json:"subtype"`
	} `json:"request"`
	Response struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
		Error     string `json:"error"`
		Response  struct {
			Version   string   `json:"claude_code_version"`
			State     string   `json:"session_state"`
			Cancelled []string `json:"cancelled"`
			Queued    []string `json:"still_queued"`
			Models    []struct {
				Value    string `json:"value"`
				Resolved string `json:"resolvedModel"`
			} `json:"models"`
		} `json:"response"`
	} `json:"response"`
}
type headlessRead struct {
	frame headlessFrame
	err   error
}

func scanHeadlessLine(data []byte, atEOF bool) (int, []byte, error) {
	if at := bytes.IndexByte(data, '\n'); at >= 0 {
		return at + 1, data[:at], nil
	}
	if atEOF && len(data) > 0 {
		return 0, nil, errors.New("truncated Claude protocol frame")
	}
	return 0, nil, nil
}
func readHeadlessFrames(ctx context.Context, r io.Reader, out chan<- headlessRead) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), maxHeadlessFrame)
	scanner.Split(scanHeadlessLine)
	send := func(v headlessRead) bool {
		select {
		case out <- v:
			return true
		case <-ctx.Done():
			return false
		}
	}
	for scanner.Scan() {
		f, dropped, err := decodeHeadlessFrame(scanner.Bytes())
		if err != nil || f.Type == "" {
			send(headlessRead{err: errors.New("invalid Claude protocol frame")})
			return
		}
		if len(dropped) > 0 {
			fmt.Fprintf(os.Stderr, "Claude %s frame fields in an unexpected shape were ignored: %s\n", f.Type, strings.Join(dropped, ", "))
		}
		if !send(headlessRead{frame: f}) {
			return
		}
	}
	err := scanner.Err()
	if err != nil {
		err = errors.New("Claude protocol frame is malformed or exceeds 16 MiB")
	} else {
		err = io.EOF
	}
	send(headlessRead{err: err})
}

// A failed pipe write is never automatically repeated: even a partial write may
// already have reached Claude. The native UUID is stable across Wi deduplication.
func writeHeadlessFrame(w *os.File, value any) (int, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return 0, errors.New("encode Claude protocol frame")
	}
	if len(data) > maxHeadlessFrame {
		return 0, errors.New("Claude input frame exceeds 16 MiB")
	}
	data = append(data, '\n')
	if err := w.SetWriteDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return 0, fmt.Errorf("set Claude input deadline: %w", err)
	}
	total := 0
	for total < len(data) {
		n, err := w.Write(data[total:])
		total += n
		if err != nil {
			return total, errors.New("Claude input delivery failed")
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}
func headlessControl(id, subtype string) map[string]any {
	return map[string]any{"type": "control_request", "request_id": id, "request": map[string]any{"subtype": subtype}}
}

// These are advertised by system.init, not the initialize response. Their
// absence means the receipt/identity contract used by this driver is missing.
func supportedHeadlessCapabilities(capabilities []string) bool {
	available := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		available[capability] = true
	}
	return available["msg_lifecycle_v1"] && available["request_marker_lists_v1"] &&
		available["interrupt_receipt_v1"] && available["interrupt_cancel_queued_v1"]
}

func supportedHeadlessVersion(version string) bool {
	var major, minor, patch int
	if _, err := fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch); err != nil {
		return false
	}
	return major > 2 || major == 2 && (minor > 1 || minor == 1 && patch >= 299)
}

// decodeHeadlessFrame decodes one frame. A newer Claude may change the shape
// of a field; rather than lose the session, fields that no longer decode are
// left out and named, and the rest of the frame is kept. Only a line that is
// not a JSON object fails.
func decodeHeadlessFrame(line []byte) (headlessFrame, []string, error) {
	var f headlessFrame
	if json.Unmarshal(line, &f) == nil {
		return f, nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return headlessFrame{}, nil, err
	}
	dropped := []string{}
	for key, value := range fields {
		var probe headlessFrame
		single, _ := json.Marshal(map[string]json.RawMessage{key: value})
		if json.Unmarshal(single, &probe) != nil {
			delete(fields, key)
			dropped = append(dropped, key)
		}
	}
	sort.Strings(dropped)
	kept, _ := json.Marshal(fields)
	f = headlessFrame{}
	err := json.Unmarshal(kept, &f)
	return f, dropped, err
}
