package cli

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/testutil"
	"github.com/regb/workitem/internal/xdg"
)

func TestEnsureDaemonRefusesIncompatibleDatabaseWithoutShutdown(t *testing.T) {
	for _, schema := range []uint64{0, coordinator.SchemaVersion - 1, coordinator.SchemaVersion + 1} {
		root := testutil.ShortTempDir(t)
		socket := filepath.Join(root, "daemon.sock")
		listener, err := net.Listen("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		methods := make(chan string, 16)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				conn.SetDeadline(time.Now().Add(time.Second))
				var request coordinator.Request
				if json.NewDecoder(conn).Decode(&request) == nil {
					methods <- request.Method
					status := coordinator.Status{Database: coordinator.DatabaseStatus{SchemaVersion: schema}}
					payload, _ := json.Marshal(status)
					json.NewEncoder(conn).Encode(coordinator.Response{ProtocolVersion: coordinator.ProtocolVersion, BuildIdentity: "other-build", ID: request.ID, OK: true, Result: payload})
				}
				conn.Close()
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, started, err := ensureDaemonRunning(ctx, xdg.Paths{DataStateRoot: filepath.Join(root, "state")}, socket, false)
		cancel()
		listener.Close()
		<-done
		close(methods)
		if err == nil || started || !strings.Contains(err.Error(), "cannot automatically replace daemon schema") {
			t.Fatalf("schema=%d started=%v error=%v", schema, started, err)
		}
		for method := range methods {
			if method != coordinator.MethodStatus {
				t.Fatalf("schema=%d sent %s", schema, method)
			}
		}
	}
}

// fakeDaemon answers every request as a daemon of another build, reporting
// status, and records the methods asked.
func fakeDaemon(t *testing.T, socket string, status coordinator.Status) <-chan string {
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	methods := make(chan string, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.SetDeadline(time.Now().Add(time.Second))
			var request coordinator.Request
			if json.NewDecoder(conn).Decode(&request) == nil {
				methods <- request.Method
				payload, _ := json.Marshal(status)
				json.NewEncoder(conn).Encode(coordinator.Response{ProtocolVersion: coordinator.ProtocolVersion, BuildIdentity: "other-build", ID: request.ID, OK: true, Result: payload})
			}
			conn.Close()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	return methods
}

// A managed daemon, configured so or reporting itself so, is never replaced
// or started by a client: the client of another build is refused, and a
// stopped managed daemon is left to its service manager.
func TestEnsureDaemonLeavesAManagedDaemonToItsServiceManager(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, test := range []struct {
		name       string
		configured bool
		reported   bool
	}{{"configured", true, false}, {"reported", false, true}} {
		root := testutil.ShortTempDir(t)
		socket := filepath.Join(root, "daemon.sock")
		methods := fakeDaemon(t, socket, coordinator.Status{Managed: test.reported, Database: coordinator.DatabaseStatus{SchemaVersion: coordinator.SchemaVersion}})
		_, started, err := ensureDaemonRunning(ctx, xdg.Paths{DataStateRoot: filepath.Join(root, "state")}, socket, test.configured)
		if err == nil || started || !strings.Contains(err.Error(), "service manager") {
			t.Fatalf("%s: started=%v error=%v", test.name, started, err)
		}
		for len(methods) > 0 {
			if method := <-methods; method != coordinator.MethodStatus {
				t.Fatalf("%s: sent %s to a managed daemon", test.name, method)
			}
		}
	}
	root := testutil.ShortTempDir(t)
	_, started, err := ensureDaemonRunning(ctx, xdg.Paths{DataStateRoot: filepath.Join(root, "state")}, filepath.Join(root, "absent.sock"), true)
	if err == nil || started || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("absent managed daemon: started=%v error=%v", started, err)
	}
}
