package driverprotocol

import (
	"net"
	"testing"
	"time"
)

// Simulate an Accept already in flight returning after Close marked the
// server done and swept its registered connections.
type lateConnectionListener struct {
	conn     net.Conn
	accepted bool
}

func (l *lateConnectionListener) Accept() (net.Conn, error) {
	if l.accepted {
		return nil, net.ErrClosed
	}
	l.accepted = true
	return l.conn, nil
}
func (*lateConnectionListener) Close() error   { return nil }
func (*lateConnectionListener) Addr() net.Addr { return &net.UnixAddr{Net: "unix"} }

func TestRuntimeSocketRejectsConnectionAcceptedDuringClose(t *testing.T) {
	for _, kind := range []string{"control", "event"} {
		t.Run(kind, func(t *testing.T) {
			conn, peer := net.Pipe()
			defer conn.Close()
			defer peer.Close()
			listener := &lateConnectionListener{conn: conn}
			done, closed := make(chan struct{}), make(chan struct{})
			close(done)
			if kind == "control" {
				s := &ControlServer{listener: listener, requests: make(chan ControlRequest), done: done, closed: closed, connections: map[net.Conn]struct{}{}}
				go s.accept()
			} else {
				s := &EventServer{listener: listener, requests: make(chan EventRequest), done: done, closed: closed, connections: map[net.Conn]struct{}{}}
				go s.accept()
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("late accepted connection blocked shutdown")
			}
		})
	}
}
