package coordinator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/regb/workitem/internal/testutil"
)

// A runtime host of another schema can neither read nor change durable
// state; health reads and runtime events stay open to hosts that predate the
// handshake. A host of another build is refused too.
func TestSchemaSkewCannotMutateDaemon(t *testing.T) {
	server, err := NewServer(t.TempDir(), filepath.Join(testutil.ShortTempDir(t), "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx := context.Background()
	before, err := server.status()
	if err != nil {
		t.Fatal(err)
	}
	refused := func(request Request, code string) {
		t.Helper()
		var protocolErr *ProtocolError
		if _, err := server.dispatch(ctx, request, AccessRuntimeHost); !errors.As(err, &protocolErr) || protocolErr.Code != code {
			t.Fatalf("schema=%d build=%q method=%s: err=%v, want %s", request.SchemaVersion, request.BuildIdentity, request.Method, err, code)
		}
	}
	for _, schema := range []uint64{0, SchemaVersion - 1, SchemaVersion + 1} {
		for _, method := range []string{MethodStoreCommand, MethodCanonicalManifest, MethodListManifests, MethodRepositorySource} {
			refused(Request{ProtocolVersion: ProtocolVersion, SchemaVersion: schema, BuildIdentity: "old-build", ID: "skewed", Method: method}, "schema_mismatch")
		}
		if _, err := server.dispatch(ctx, Request{ProtocolVersion: ProtocolVersion, SchemaVersion: schema, ID: "health", Method: MethodPing}, AccessRuntimeHost); err != nil {
			t.Fatalf("schema %d health read: %v", schema, err)
		}
	}
	refused(Request{ProtocolVersion: ProtocolVersion, SchemaVersion: SchemaVersion, BuildIdentity: "other-build", ID: "skewed", Method: MethodStoreCommand}, "build_mismatch")
	after, err := server.status()
	if err != nil || before.Database.GlobalSequence != after.Database.GlobalSequence {
		t.Fatalf("daemon changed: before=%+v after=%+v error=%v", before, after, err)
	}
}

func TestStatusRetainsSchemaOnBuildMismatch(t *testing.T) {
	root, runtime := t.TempDir(), testutil.ShortTempDir(t)
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
