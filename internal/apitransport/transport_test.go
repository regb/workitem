package apitransport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/gen/wi/core/v1/corev1connect"
	"github.com/regb/workitem/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(testutil.RunIsolatedTests(m.Run))
}

type systemFixture struct {
	corev1connect.UnimplementedSystemServiceHandler
}

func (systemFixture) GetInfo(ctx context.Context, _ *connect.Request[corev1.GetInfoRequest]) (*connect.Response[corev1.GetInfoResponse], error) {
	return connect.NewResponse(&corev1.GetInfoResponse{ApiVersion: "wi.core.v1", DefaultAgentProfile: string(Caller(ctx))}), nil
}

func (systemFixture) GetHealth(context.Context, *connect.Request[corev1.GetHealthRequest]) (*connect.Response[corev1.GetHealthResponse], error) {
	err := connect.NewError(connect.CodeFailedPrecondition, errors.New("setup failed after creation"))
	detail, detailErr := connect.NewErrorDetail(&corev1.PartialOutcome{ItemId: "retained-item", Completed: []string{"created item"}})
	if detailErr != nil {
		return nil, detailErr
	}
	err.AddDetail(detail)
	return nil, err
}

type eventFixture struct {
	cancelled chan struct{}
}

func (f eventFixture) WatchChanges(ctx context.Context, req *connect.Request[corev1.WatchChangesRequest], stream *connect.ServerStream[corev1.WatchChangesResponse]) error {
	defer close(f.cancelled)
	if err := stream.Send(&corev1.WatchChangesResponse{Cursor: "opaque-cursor", ItemIds: []string{req.Msg.After}, All: true}); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func startServer(t *testing.T, access Access, handler http.Handler) (string, *Clients) {
	t.Helper()
	// A short path is required on macOS, including when the caller's TMPDIR is long.
	root, err := os.MkdirTemp("/tmp", "wi-api-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	path := filepath.Join(root, "api.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	server, err := NewServer(handler, access)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Close()
		select {
		case err := <-done:
			if !errors.Is(err, http.ErrServerClosed) {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	})
	clients, err := NewClients(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(clients.Close)
	return path, clients
}

func TestConnectJSONAndCallerIdentity(t *testing.T) {
	for _, access := range []Access{Operator, Agent} {
		t.Run(string(access), func(t *testing.T) {
			path, handler := corev1connect.NewSystemServiceHandler(systemFixture{}, connect.WithCodec(JSONCodec{}))
			mux := http.NewServeMux()
			mux.Handle(path, handler)
			_, clients := startServer(t, access, mux)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req := connect.NewRequest(&corev1.GetInfoRequest{})
			req.Header().Set("Wi-Caller", "operator")
			result, err := clients.System.GetInfo(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if result.Msg.ApiVersion != "wi.core.v1" || result.Msg.DefaultAgentProfile != string(access) {
				t.Fatalf("unexpected response: %v", result.Msg)
			}
			if clients.Items == nil || clients.Lifecycle == nil || clients.Workspaces == nil || clients.Agents == nil || clients.Control == nil || clients.Repositories == nil || clients.Profiles == nil || clients.Jobs == nil || clients.Events == nil || clients.Views == nil {
				t.Fatal("missing generated service client")
			}
		})
	}
}

func TestAllProtocolsAndPartialOutcome(t *testing.T) {
	_, handler := corev1connect.NewSystemServiceHandler(systemFixture{}, connect.WithCodec(JSONCodec{}))
	_, clients := startServer(t, Operator, handler)
	for _, protocol := range []struct {
		name   string
		option connect.ClientOption
	}{
		{"connect-json", connect.WithProtoJSON()},
		{"connect-protobuf", connect.WithClientOptions()},
		{"grpc", connect.WithGRPC()},
		{"grpc-web", connect.WithGRPCWeb()},
	} {
		t.Run(protocol.name, func(t *testing.T) {
			client := corev1connect.NewSystemServiceClient(&http.Client{Transport: clients.transport}, baseURL, protocol.option)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			info, err := client.GetInfo(ctx, connect.NewRequest(&corev1.GetInfoRequest{}))
			if err != nil || info.Msg.ApiVersion != "wi.core.v1" {
				t.Fatalf("info = %v, err = %v", info, err)
			}
			_, err = client.GetHealth(ctx, connect.NewRequest(&corev1.GetHealthRequest{}))
			if connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("error code: %v", err)
			}
			var rpcErr *connect.Error
			if !errors.As(err, &rpcErr) || len(rpcErr.Details()) != 1 {
				t.Fatalf("missing partial outcome: %v", err)
			}
			value, err := rpcErr.Details()[0].Value()
			if err != nil {
				t.Fatal(err)
			}
			partial, ok := value.(*corev1.PartialOutcome)
			if !ok || partial.ItemId != "retained-item" || len(partial.Completed) != 1 {
				t.Fatalf("incorrect partial outcome: %v", value)
			}
		})
	}
}

func TestHTTP1JSONUsesProtoFieldNames(t *testing.T) {
	_, handler := corev1connect.NewSystemServiceHandler(systemFixture{}, connect.WithCodec(JSONCodec{}))
	path, _ := startServer(t, Operator, handler)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	for _, body := range []string{"{}", `{"not_in_the_contract":true}`, "{"} {
		req, err := http.NewRequest(http.MethodPost, baseURL+corev1connect.SystemServiceGetInfoProcedure, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if body == "{}" {
			if response.StatusCode != http.StatusOK || !bytes.Contains(data, []byte(`"api_version"`)) || bytes.Contains(data, []byte(`"apiVersion"`)) {
				t.Fatalf("response = %d %s", response.StatusCode, data)
			}
		} else if response.StatusCode != http.StatusBadRequest || !bytes.Contains(data, []byte(`"invalid_argument"`)) {
			t.Fatalf("malformed request response = %d %s", response.StatusCode, data)
		}
	}
}

func TestStreamCancellationDoesNotStopServer(t *testing.T) {
	fixture := eventFixture{cancelled: make(chan struct{})}
	mux := http.NewServeMux()
	mux.Handle(corev1connect.NewEventServiceHandler(fixture, connect.WithCodec(JSONCodec{})))
	mux.Handle(corev1connect.NewSystemServiceHandler(systemFixture{}, connect.WithCodec(JSONCodec{})))
	_, clients := startServer(t, Operator, mux)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	streamCtx, cancelStream := context.WithCancel(ctx)
	stream, err := clients.Events.WatchChanges(streamCtx, connect.NewRequest(&corev1.WatchChangesRequest{After: "client-cursor"}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if !stream.Receive() || stream.Msg().Cursor != "opaque-cursor" || stream.Msg().ItemIds[0] != "client-cursor" {
		t.Fatalf("first snapshot: %v, %v", stream.Msg(), stream.Err())
	}
	cancelStream()
	if stream.Receive() || connect.CodeOf(stream.Err()) != connect.CodeCanceled {
		t.Fatalf("stream cancellation: %v", stream.Err())
	}
	select {
	case <-fixture.cancelled:
	case <-ctx.Done():
		t.Fatal("stream cancellation did not reach server")
	}
	if _, err := clients.System.GetInfo(ctx, connect.NewRequest(&corev1.GetInfoRequest{})); err != nil {
		t.Fatalf("stream cancellation stopped the server: %v", err)
	}
}

func TestTransportRejectsInvalidConfiguration(t *testing.T) {
	for _, path := range []string{"", "relative.sock"} {
		if _, err := NewClients(path); err == nil {
			t.Fatalf("accepted socket path %q", path)
		}
	}
	if _, err := NewServer(http.NotFoundHandler(), Access("untrusted")); err == nil {
		t.Fatal("accepted invalid endpoint identity")
	}
	if Caller(context.Background()) != "" {
		t.Fatal("unauthenticated context has an identity")
	}
	if _, err := (JSONCodec{}).Marshal(struct{}{}); err == nil {
		t.Fatal("marshalled a non-protobuf value")
	}
	if err := (JSONCodec{}).Unmarshal([]byte("{}"), &struct{}{}); err == nil {
		t.Fatal("unmarshalled a non-protobuf value")
	}
}
