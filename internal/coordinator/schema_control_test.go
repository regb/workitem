package coordinator

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"
)

func TestSchemaSkewCannotStopOrMutateDaemon(t *testing.T) {
	root, runtime := t.TempDir(), t.TempDir()
	socket, err := SocketPath(runtime, root)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(root, socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	t.Cleanup(func() { server.Close(); <-done })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := &Client{SocketPath: socket}
	before, err := client.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []uint64{0, SchemaVersion - 1, SchemaVersion + 1} {
		for _, method := range []string{MethodShutdown, MethodCreateItem, MethodManifestCommand, MethodEditItem, MethodStoreCommand, MethodSetRepositoryArchived} {
			conn, err := net.DialTimeout("unix", socket, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			conn.SetDeadline(time.Now().Add(time.Second))
			request := Request{ProtocolVersion: ProtocolVersion, SchemaVersion: schema, BuildIdentity: "old-build", ID: "skewed", Method: method}
			if err := json.NewEncoder(conn).Encode(request); err != nil {
				conn.Close()
				t.Fatal(err)
			}
			var response Response
			err = json.NewDecoder(conn).Decode(&response)
			conn.Close()
			if err != nil || response.OK || response.Error == nil || response.Error.Code != "schema_mismatch" {
				t.Fatalf("schema=%d method=%s response=%+v error=%v", schema, method, response, err)
			}
		}
	}
	after, err := client.Status(ctx)
	if err != nil || before.PID != after.PID || before.Database.GlobalSequence != after.Database.GlobalSequence {
		t.Fatalf("daemon changed: before=%+v after=%+v error=%v", before, after, err)
	}
	// Same-schema rebuilds can still replace the daemon normally.
	if err := (&Client{SocketPath: socket, BuildIdentity: "another-build"}).Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestStatusRetainsSchemaOnBuildMismatch(t *testing.T) {
	root, runtime := t.TempDir(), t.TempDir()
	socket, _ := SocketPath(runtime, root)
	server, err := NewServer(root, socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	t.Cleanup(func() { server.Close(); <-done })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status, err := (&Client{SocketPath: socket, BuildIdentity: "other-build"}).Status(ctx)
	if err == nil || status.Database.SchemaVersion != SchemaVersion || status.PID != os.Getpid() {
		t.Fatalf("status=%+v error=%v", status, err)
	}
}
