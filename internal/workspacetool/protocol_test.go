package workspacetool

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

func TestExchange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool.sock")
	server, err := Listen(path, func(_ context.Context, request Request) (Response, error) {
		return Response{ExitCode: 7, Stdout: append([]byte(request.CWD+":"), request.Stdin...)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	response, err := Exchange(context.Background(), path, Request{CWD: "/workspace", Command: []string{"cat"}, Stdin: []byte("input")})
	if err != nil {
		t.Fatal(err)
	}
	if response.ExitCode != 7 || !bytes.Equal(response.Stdout, []byte("/workspace:input")) {
		t.Fatalf("response=%+v", response)
	}
}

func TestExchangeReportsHandlerError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool.sock")
	server, err := Listen(path, func(context.Context, Request) (Response, error) {
		return Response{}, context.Canceled
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if _, err := Exchange(context.Background(), path, Request{Command: []string{"true"}}); err == nil {
		t.Fatal("expected handler error")
	}
}
