package pi

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type sessionHeader struct {
	Type          string `json:"type"`
	Version       int    `json:"version"`
	ID            string `json:"id"`
	Timestamp     string `json:"timestamp"`
	CWD           string `json:"cwd"`
	ParentSession string `json:"parentSession,omitempty"`
}

func (c Client) SessionCWD(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open Pi session: %w", err)
	}
	defer file.Close()
	line, err := bufio.NewReader(file).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read Pi session header: %w", err)
	}
	if strings.TrimSpace(line) == "" {
		return "", nil
	}
	var header sessionHeader
	if err := json.Unmarshal([]byte(line), &header); err != nil {
		return "", fmt.Errorf("parse Pi session header: %w", err)
	}
	if header.Type != "session" {
		return "", fmt.Errorf("session header has type %q", header.Type)
	}
	return strings.TrimSpace(header.CWD), nil
}

// ForkSession mirrors Pi SessionManager.forkFrom while allowing wi to choose a
// deterministic target path before the runtime is launched.
func (c Client) ForkSession(ctx context.Context, sourcePath, targetPath, targetCWD string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	sourcePath, err = filepath.Abs(sourcePath)
	if err != nil {
		return err
	}
	targetPath, err = filepath.Abs(targetPath)
	if err != nil {
		return err
	}
	targetCWD, err = filepath.Abs(targetCWD)
	if err != nil {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open source Pi session: %w", err)
	}
	defer source.Close()
	reader := bufio.NewReader(source)
	line, readErr := reader.ReadString('\n')
	if readErr != nil && readErr != io.EOF {
		return fmt.Errorf("read source Pi session header: %w", readErr)
	}
	if strings.TrimSpace(line) == "" {
		return fmt.Errorf("source Pi session is empty or invalid: %s", sourcePath)
	}
	var sourceHeader sessionHeader
	if err := json.Unmarshal([]byte(line), &sourceHeader); err != nil {
		return fmt.Errorf("parse source Pi session header: %w", err)
	}
	if sourceHeader.Type != "session" {
		return fmt.Errorf("source Pi session header has type %q", sourceHeader.Type)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(targetPath), ".pi-fork-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	sessionID, err := newSessionUUID()
	if err != nil {
		return err
	}
	header := sessionHeader{Type: "session", Version: 3, ID: sessionID, Timestamp: time.Now().UTC().Format(time.RFC3339Nano), CWD: targetCWD, ParentSession: sourcePath}
	if err := json.NewEncoder(tmp).Encode(header); err != nil {
		return err
	}
	if readErr != io.EOF {
		if _, err := io.Copy(tmp, reader); err != nil {
			return err
		}
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Link(tmpPath, targetPath); err != nil {
		return fmt.Errorf("publish forked Pi session: %w", err)
	}
	return nil
}

func newSessionUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}
