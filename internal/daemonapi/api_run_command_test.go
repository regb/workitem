package daemonapi_test

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
)

// A person runs a command in an item's checkout as in a terminal opened
// there: its output is the job's log tail, its exit status the job's
// result, and it runs as the person, with no agent credential.
func TestRunCommandRunsInTheCheckoutAsThePerson(t *testing.T) {
	t.Parallel()
	f := apitest.New(t)
	repo := f.Repository(t, "test")
	call(t, f.Clients.Repositories.UpdateEnvironment, &v1.UpdateEnvironmentRequest{RepositoryId: repo, Set: []*v1.EnvironmentVariable{{Key: "GREETING", Value: "hello from the repository"}}})
	item, checkout := activeItem(t, f, repo, "ran")
	run := func(t *testing.T, request *v1.RunCommandRequest) *v1.Job {
		t.Helper()
		request.ItemId = item.Id
		job := apitest.Wait(t, f.Clients, call(t, f.Clients.Workspaces.RunCommand, request).Job)
		if job.Kind != "run_command" || job.State != v1.JobState_JOB_STATE_SUCCEEDED || job.GetCommand() == nil {
			t.Fatalf("job: %v", job)
		}
		return job
	}
	job := run(t, &v1.RunCommandRequest{Command: `echo "$GREETING"; pwd -P; echo "id=$WI_ID"; echo "credential=${WI_ITEM_CREDENTIAL_FILE:-none}"; echo oops >&2; exit 3`})
	if code := job.GetCommand().ExitCode; code != 3 {
		t.Fatalf("exit code %d", code)
	}
	for _, want := range []string{"hello from the repository", checkout.Claim.Path, "id=" + item.Id, "credential=none", "oops"} {
		if !strings.Contains(job.LogTail, want) {
			t.Errorf("output lacks %q:\n%s", want, job.LogTail)
		}
	}
	stopped := run(t, &v1.RunCommandRequest{Command: "echo started; sleep 30", TimeoutSeconds: 1})
	if stopped.GetCommand().ExitCode != -1 || !strings.Contains(stopped.LogTail, "started") || !strings.Contains(stopped.LogTail, "stopped after 1s") {
		t.Fatalf("timed out: %v", stopped)
	}
	refused(t, connect.CodeInvalidArgument, f.Clients.Workspaces.RunCommand, &v1.RunCommandRequest{ItemId: item.Id, Command: "  "})
	backlog := newItem(t, f, repo, "no checkout")
	refused(t, connect.CodeFailedPrecondition, f.Clients.Workspaces.RunCommand, &v1.RunCommandRequest{ItemId: backlog.Id, Command: "true"})
	// Running commands as a person is a person's.
	agent, _ := f.AgentClients(t, item.Id)
	refused(t, connect.CodePermissionDenied, agent.Workspaces.RunCommand, &v1.RunCommandRequest{ItemId: item.Id, Command: "true"})
}
