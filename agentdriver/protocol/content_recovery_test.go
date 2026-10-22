package driverprotocol

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestContentEndpointIsRuntimeScopedAfterCrash(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "wi-content-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	old := ContentSocketPath(filepath.Join(root, "old-owner.sock"))
	next := ContentSocketPath(filepath.Join(root, "next-owner.sock"))
	if old == next {
		t.Fatal("successive owners share a content endpoint")
	}
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: old, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	_ = stale.Close()
	server, err := ListenContent(next, "next", "item", true)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if _, err := ReadContent(context.Background(), next, "next", "item"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("predecessor evidence was removed", err)
	}
}

func TestContentVersionDoesNotFollowBootstrapVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.sock")
	server, err := ListenContent(path, "runtime", "item", true)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	value, err := ReadContent(context.Background(), path, "runtime", "item")
	if err != nil {
		t.Fatal(err)
	}
	if value.ProtocolVersion != ContentVersion {
		t.Fatalf("content version = %d", value.ProtocolVersion)
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(ContentRequest{ProtocolVersion: ContentVersion + 1, RuntimeID: "runtime", WorkItemID: "item"}); err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(conn).Decode(&value); err != nil {
		t.Fatal(err)
	}
	if value.Error == "" {
		t.Fatal("unsupported content version accepted")
	}
}

func TestContentTruncationPreservesUnicodeAndResets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.sock")
	server, err := ListenContent(path, "runtime", "item", true)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.Update(EventMessageDelta, strings.Repeat("x", MaxContentTextBytes-1)+"🌍", "")
	value, err := ReadContent(context.Background(), path, "runtime", "item")
	if err != nil {
		t.Fatal(err)
	}
	if !value.Truncated || !utf8.ValidString(value.Text) || strings.ContainsRune(value.Text, utf8.RuneError) {
		t.Fatalf("bad truncation, bytes=%d", len(value.Text))
	}
	server.Update("message.started", "", "")
	server.Update(EventMessageDelta, "hello 🌍", "")
	value, err = ReadContent(context.Background(), path, "runtime", "item")
	if err != nil || value.Truncated || value.Text != "hello 🌍" {
		t.Fatalf("new message %+v %v", value, err)
	}
}
