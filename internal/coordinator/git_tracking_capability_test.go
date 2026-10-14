package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/testutil"
)

func TestGitTrackingClientsRefuseLegacyDaemonBeforeMutations(t *testing.T) {
	socket := filepath.Join(testutil.ShortTempDir(t), "old.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 3; i++ {
			conn, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			var request Request
			if err := json.NewDecoder(conn).Decode(&request); err != nil {
				conn.Close()
				done <- err
				return
			}
			if request.Method != MethodRepositorySource {
				conn.Close()
				done <- fmt.Errorf("mutating operation reached legacy daemon")
				return
			}
			// No upstream_source acknowledgement: the older daemon ignores the new
			// observe_upstream query and reports its stored repository metadata.
			result := json.RawMessage(`{"repository":{"id":"` + importTestID + `","name":"project","integration_branch":"main"}}`)
			err = json.NewEncoder(conn).Encode(Response{ProtocolVersion: ProtocolVersion, BuildIdentity: request.BuildIdentity, ID: request.ID, OK: true, Result: result})
			conn.Close()
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	client := Client{SocketPath: socket}
	ctx := context.Background()
	_, a := client.SetRepositoryUpstream(ctx, SetRepositoryUpstreamCommand{RepositoryID: importTestID})
	_, b := client.SyncRepository(ctx, RepositorySyncRequest{Selector: "project"})
	_, c := client.RepositoryStatus(ctx, RepositoryStatusRequest{Selector: "project", Fetch: true})
	for _, err := range []error{a, b, c} {
		if err == nil || !strings.Contains(err.Error(), "restart") {
			t.Fatalf("err=%v", err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
