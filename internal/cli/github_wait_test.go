package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitransport"
	"github.com/regb/workitem/plugins/github"
)

func TestGitHubWaitUsesCurrentCLIContract(t *testing.T) {
	f := newClientFixture(t)
	item := f.create(t, "GitHub wait")
	f.cfg.Env["WI_ID"] = item.Id
	state := `{"state":"OPEN"}`
	var output bytes.Buffer
	plugin := github.Plugin{Wi: "wi", Gh: "gh", Out: &output, RepositoryID: f.repo}
	plugin.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "gh" {
			if args[0] == "api" {
				return []byte("waiting-user\n"), nil
			}
			return []byte(state), nil
		}
		f.out.Reset()
		f.errs.Reset()
		if code := Run(ctx, args, f.cfg); code != ExitOK {
			return nil, fmt.Errorf("wi %v: exit %d: %s", args, code, f.errs)
		}
		return bytes.Clone(f.out.Bytes()), nil
	}
	ctx := context.Background()
	if err := plugin.Wait(ctx, []string{"https://github.com/NetFabric-AI/netfabric-code/pull/3556", "--json"}); err != nil {
		t.Fatal(err)
	}
	var shown v1.GetItemResponse
	f.decode(t, f.run(t, "show", "--json"), &shown)
	if len(shown.Item.Blockers) != 1 || shown.Item.Blockers[0].Meta[github.MetaPR] != "NetFabric-AI/netfabric-code#3556" {
		t.Fatal(shown.String())
	}
	blocker := shown.Item.Blockers[0]
	// Historical comments must not clear a newly created wait.
	state = fmt.Sprintf(`{"state":"OPEN","comments":[{"author":{"login":"reviewer"},"createdAt":%q}]}`, blocker.CreatedAt.AsTime().Add(-time.Hour).Format(time.RFC3339Nano))
	if err := plugin.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	f.decode(t, f.run(t, "show", "--json"), &shown)
	if len(shown.Item.Blockers) != 1 {
		t.Fatal("historical comment cleared a new wait", shown.String())
	}
	state = fmt.Sprintf(`{"state":"OPEN","reviews":[{"author":{"login":"reviewer"},"state":"APPROVED","submittedAt":%q}]}`, blocker.CreatedAt.AsTime().Add(time.Second).Format(time.RFC3339Nano))
	if err := plugin.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	f.decode(t, f.run(t, "show", "--json"), &shown)
	if len(shown.Item.Blockers) != 0 || !strings.Contains(output.String(), "approved review by reviewer") {
		t.Fatalf("wait not cleared: %s\n%s", shown.String(), output.String())
	}
	// The plugin must propagate the agent endpoint's refusal, not bypass it.
	client, err := apitransport.NewClients(f.api.AgentSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	f.cfg.API = client
	if err := plugin.Wait(ctx, []string{"https://github.com/NetFabric-AI/netfabric-code/pull/3556"}); err == nil {
		t.Fatal("restricted endpoint allowed adding a GitHub blocker")
	}
}
