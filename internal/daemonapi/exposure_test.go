package daemonapi

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitransport"
	"github.com/regb/workitem/internal/coordinator"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// Every procedure of the API is either open to the web listener or refused
// with a reason: adding an RPC forces the decision.
func TestEveryProcedureIsClassifiedForTheWebListener(t *testing.T) {
	_ = v1.File_wi_core_v1_items_proto // registers the API's files
	seen := map[string]bool{}
	protoregistry.GlobalFiles.RangeFilesByPackage("wi.core.v1", func(file protoreflect.FileDescriptor) bool {
		services := file.Services()
		for i := range services.Len() {
			methods := services.Get(i).Methods()
			for j := range methods.Len() {
				procedure := "/" + string(services.Get(i).FullName()) + "/" + string(methods.Get(j).Name())
				seen[procedure] = true
				_, refused := webRefused[procedure]
				if webProcedures[procedure] == refused {
					t.Errorf("%s must be in exactly one of webProcedures and webRefused", procedure)
				}
			}
		}
		return true
	})
	if len(seen) < 100 {
		t.Fatalf("found only %d procedures", len(seen))
	}
	for procedure := range webProcedures {
		if !seen[procedure] {
			t.Errorf("webProcedures names unknown %s", procedure)
		}
	}
	for procedure := range webRefused {
		if !seen[procedure] {
			t.Errorf("webRefused names unknown %s", procedure)
		}
	}
	for _, refused := range []string{"/wi.core.v1.SystemService/Shutdown", "/wi.core.v1.ItemService/DeleteItem", "/wi.core.v1.SystemService/GetConfig", "/wi.core.v1.RepositoryService/ReadRepositoryFile"} {
		if webAllowed(refused) {
			t.Errorf("%s is open to the web", refused)
		}
	}
}

// An upstream URL never carries its credentials out of the daemon, to any
// client, and links its page on a known host.
func TestUpstreamsAreReturnedWithoutCredentials(t *testing.T) {
	got := upstream("https://user:secret-token@github.com/regb/workitem.git", "main")
	if got.Url != "https://github.com/regb/workitem.git" || got.WebUrl != "https://github.com/regb/workitem/tree/main" || got.Host != "github" || strings.Contains(got.String(), "secret-token") {
		t.Fatalf("%v", got)
	}
	if scp := upstream("deploy@gitlab.com:group/app.git", ""); scp.Url != "gitlab.com:group/app.git" || scp.Host != "gitlab" {
		t.Fatalf("%v", scp)
	}
}

// Through the web listener, a refused call is denied, the user wi web vouches
// for is the caller's label, and the page cannot choose a control label.
func TestTheWebListenerEnforcesItsExposure(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	web, err := apitransport.NewClients(coordinator.WebSocketPath(filepath.Join(f.root, "api.sock")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := web.System.Shutdown(ctx, connect.NewRequest(&v1.ShutdownRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("shutdown through the web listener: %v", err)
	}
	if _, err := web.System.GetConfig(ctx, connect.NewRequest(&v1.GetConfigRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("config through the web listener: %v", err)
	}
	info := connect.NewRequest(&v1.GetInfoRequest{})
	info.Header().Set(LabelHeader, "alice@example.com")
	got, err := web.System.GetInfo(ctx, info)
	if err != nil || got.Msg.Caller.Label != "alice@example.com" || got.Msg.Caller.Kind != v1.CallerKind_CALLER_KIND_WEB_ADAPTER {
		t.Fatalf("web caller: %v %v", got, err)
	}
	// The operator listener does not believe the header.
	operator := connect.NewRequest(&v1.GetInfoRequest{})
	operator.Header().Set(LabelHeader, "alice@example.com")
	if got, err := f.clients.System.GetInfo(ctx, operator); err != nil || got.Msg.Caller.Label != "" {
		t.Fatalf("operator caller: %v %v", got, err)
	}
}
