package daemonapi_test

import (
	"context"
	"testing"

	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/credential"
	"github.com/regb/workitem/internal/daemonapi"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitransport"
)

// With wi web's credential, a refused call is denied and the user wi web
// vouches for is the caller's label.
func TestWebCredentialEnforcesItsExposure(t *testing.T) {
	f := apitest.New(t)
	ctx := context.Background()
	web, err := apitransport.NewClients(f.Socket, credential.Source(credential.ProxyFile(f.DataRoot()), credential.Proxy))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := web.System.Shutdown(ctx, connect.NewRequest(&v1.ShutdownRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("shutdown as wi web: %v", err)
	}
	if _, err := web.System.GetConfig(ctx, connect.NewRequest(&v1.GetConfigRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("config as wi web: %v", err)
	}
	info := connect.NewRequest(&v1.GetInfoRequest{})
	info.Header().Set(daemonapi.LabelHeader, "alice@example.com")
	got, err := web.System.GetInfo(ctx, info)
	if err != nil || got.Msg.Caller.Label != "alice@example.com" || got.Msg.Caller.Kind != v1.CallerKind_CALLER_KIND_WEB_ADAPTER {
		t.Fatalf("web caller: %v %v", got, err)
	}
	// Only wi web's credential makes the header believed.
	operator := connect.NewRequest(&v1.GetInfoRequest{})
	operator.Header().Set(daemonapi.LabelHeader, "alice@example.com")
	if got, err := f.Clients.System.GetInfo(ctx, operator); err != nil || got.Msg.Caller.Label != "" {
		t.Fatalf("operator caller: %v %v", got, err)
	}
}
