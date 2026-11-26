package socketpeer

import (
	"net"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/testutil"
)

// Validate reads the peer's credentials from a real socket and accepts only
// the current user; another user's UID is refused.
func TestValidate(t *testing.T) {
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(testutil.ShortTempDir(t), "s.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptUnix()
		if err == nil {
			err = Validate(conn)
			conn.Close()
		}
		accepted <- err
	}()
	client, err := net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := Validate(client); err != nil {
		t.Fatalf("client side: %v", err)
	}
	if err := <-accepted; err != nil {
		t.Fatalf("server side: %v", err)
	}
	if err := validatePeerUID(1001, 1000); err == nil {
		t.Fatal("mismatched peer uid was accepted")
	}
}
