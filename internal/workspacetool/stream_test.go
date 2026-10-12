package workspacetool

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/regb/workitem/internal/testutil"
)

func TestStreamArrivesBeforeCompletion(t *testing.T) {
	path := filepath.Join(testutil.ShortTempDir(t), "tool.sock")
	proceed := make(chan struct{})
	server, err := ListenStream(path, func(ctx context.Context, r Request, emit func(Response) error) (Response, error) {
		if err := emit(Response{Stdout: []byte("first")}); err != nil {
			return Response{}, err
		}
		select {
		case <-proceed:
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
		return Response{ExitCode: 7, Stdout: []byte("last")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var text string
	response, err := ExchangeStream(ctx, path, Request{}, func(r Response) error {
		text += string(r.Stdout)
		if text == "first" {
			close(proceed)
		}
		return nil
	})
	if err != nil || text != "firstlast" || response.ExitCode != 7 {
		t.Fatalf("%+v %q %v", response, text, err)
	}
}

func TestDisconnectCancelsOnlyItsHandler(t *testing.T) {
	path := filepath.Join(testutil.ShortTempDir(t), "tool.sock")
	started, stopped := make(chan struct{}), make(chan struct{})
	server, err := ListenStream(path, func(ctx context.Context, r Request, _ func(Response) error) (Response, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return Response{}, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _, _ = Exchange(ctx, path, Request{}); close(done) }()
	<-started
	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("caller disconnect did not cancel handler")
	}
	<-done
}

func TestCloseInterruptsPartialRequest(t *testing.T) {
	path := filepath.Join(testutil.ShortTempDir(t), "tool.sock")
	server, err := Listen(path, func(context.Context, Request) (Response, error) {
		t.Error("handler received partial request")
		return Response{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(`{"protocol_version":`))
	done := make(chan struct{})
	go func() { _ = server.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close blocked on partial request")
	}
}
