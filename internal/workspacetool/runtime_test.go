package workspacetool

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

func TestRuntimeInspectAndExec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool.sock")
	requests := make(chan Request, 4)
	server, err := Listen(path, func(_ context.Context, request Request) (Response, error) {
		requests <- request
		return Response{Stdout: append([]byte("out:"), request.Stdin...), Stderr: []byte("err"), ExitCode: 3}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	t.Setenv(SocketEnvironment, path)
	t.Setenv(TargetEnvironment, "claim-1")
	t.Setenv(CWDEnvironment, "/remote/workspace")

	var stdout, stderr bytes.Buffer
	code, err := RunRuntime(context.Background(), []string{"container", "inspect", "-f", "{{.State.Running}}", "claim-1"}, bytes.NewReader(nil), &stdout, &stderr)
	if err != nil || code != 3 {
		t.Fatalf("inspect code=%d err=%v stdout=%q stderr=%q", code, err, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code, err = RunRuntime(context.Background(), []string{"container", "exec", "-i", "-w", "/remote/workspace/sub", "-e", "MODE=test", "claim-1", "bash", "-c", "cat"}, bytes.NewBufferString("input"), &stdout, &stderr)
	if err != nil || code != 3 || stdout.String() != "out:input" || stderr.String() != "err" {
		t.Fatalf("exec code=%d err=%v stdout=%q stderr=%q", code, err, stdout.String(), stderr.String())
	}
	request := <-requests
	if request.CWD != "/remote/workspace" || request.Command[0] != "test" {
		t.Fatalf("inspect request=%+v", request)
	}
	request = <-requests
	if request.CWD != "/remote/workspace/sub" || request.Command[0] != "bash" || string(request.Stdin) != "input" || request.Environment["MODE"] != "test" {
		t.Fatalf("exec request=%+v", request)
	}
}

func TestRuntimeRejectsDifferentTarget(t *testing.T) {
	t.Setenv(SocketEnvironment, filepath.Join(t.TempDir(), "missing.sock"))
	t.Setenv(TargetEnvironment, "claim-1")
	t.Setenv(CWDEnvironment, "/remote/workspace")
	code, err := RunRuntime(context.Background(), []string{"container", "inspect", "claim-2"}, bytes.NewReader(nil), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || code != 1 {
		t.Fatalf("code=%d err=%v", code, err)
	}
}
