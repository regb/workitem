package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/regb/workitem/internal/executor"
	"github.com/regb/workitem/internal/executor/executortest"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/workspacetool"
)

func TestDockerExecutorLifecycle(t *testing.T) {
	if os.Getenv("WI_TEST_DOCKER") != "1" {
		t.Skip("set WI_TEST_DOCKER=1 with wi-executor-test image available")
	}
	testExecutorLifecycle(t, executor.Profile{Driver: "docker", Image: "wi-executor-test"})
}
func TestSSHExecutorLifecycle(t *testing.T) {
	s := executortest.SSHServer(t)
	testExecutorLifecycle(t, executor.Profile{Driver: "ssh", Host: s.Host, Root: s.Root, SSHConfig: s.Config})
}
func testExecutorLifecycle(t *testing.T, profile executor.Profile) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := executor.Git(ctx, repo, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	git("init", "-b", "main")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@localhost")
	if err := os.WriteFile(filepath.Join(repo, "base"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "base")
	st := store.New(t.TempDir())
	a := New(st, gitpkg.New("git"))
	a.MasterID = "docker-test"
	a.ExecutorsConfig = map[string]executor.Profile{"test": profile}
	created, err := a.NewWorkItem(ctx, NewWorkItemOptions{Title: "Executor lifecycle", CWD: repo, ExecutorProfile: "test"})
	if err != nil {
		t.Fatal(err)
	}
	opts := ResolveOptions{Selector: created.Manifest.ID, CWD: repo}
	ensured, err := a.EnsureWorkItemWorkspace(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	m := ensured.Manifest
	// Changing a named profile cannot redirect an already materialized claim.
	a.ExecutorsConfig["test"] = executor.Profile{Driver: "ssh", Host: "unreachable.invalid", Root: "/tmp/unused"}
	pinned, err := a.EnsureWorkItemWorkspace(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if pinned.Manifest.Checkout.Claim.Path != m.Checkout.Claim.Path || pinned.Manifest.Checkout.Claim.Executor.Driver != profile.Driver {
		t.Fatal("configuration redirected live claim")
	}
	a.ExecutorsConfig["test"] = profile
	if m.Checkout.Specification.Kind != model.WorkspaceKindExecutor || m.PrimaryConversation != nil {
		t.Fatalf("manifest %+v", m)
	}
	client, err := a.executorClient(m)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Destroy(context.Background()); err != nil {
			t.Error(err)
		}
	})
	run := func(command string) {
		t.Helper()
		var out, stderr bytes.Buffer
		code, err := client.Exec(ctx, client.Workdir(), []string{"bash", "-c", command}, nil, &out, &stderr)
		if err != nil || code != 0 {
			t.Fatalf("exec %q: %d %v %s", command, code, err, stderr.String())
		}
	}
	run("git fetch origin; printf change > feature; git add feature; git commit -m feature")
	if _, err := os.Stat(filepath.Join(repo, "feature")); !os.IsNotExist(err) {
		t.Fatal("executor file appeared in host checkout before merge")
	}
	// Settled-turn export imports commits without touching the host branch.
	owner := model.AgentRuntime{ID: "sync-owner", WorkItemID: m.ID}
	if err := st.SaveAgentRuntime(ctx, m.ID, owner); err != nil {
		t.Fatal(err)
	}
	if err := a.syncExecutor(ctx, m.ID, "superseded", m.Checkout.Claim.ID); err != nil {
		t.Fatal(err)
	}
	before, err := st.LoadManifest(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Checkout.Claim.Executor.ExportedHead != "" {
		t.Fatal("superseded runtime exported work")
	}
	wake, stop := a.settledExecutorSync(ctx, m.ID, owner.ID, m.Checkout.Claim.ID)
	wake <- struct{}{}
	for {
		saved, err := st.LoadManifest(m.ID)
		if err != nil {
			stop()
			t.Fatal(err)
		}
		if saved.Checkout.Claim.Executor.ExportedHead != "" {
			break
		}
		if ctx.Err() != nil {
			stop()
			t.Fatal(ctx.Err())
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	// Force a rebase by advancing the host target independently.
	if err := os.WriteFile(filepath.Join(repo, "host"), []byte("host\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "host")
	m.State = model.StateWorking
	if err := st.SaveManifest(ctx, m); err != nil {
		t.Fatal(err)
	}
	merged, err := a.MergeWorkItem(ctx, MergeOptions{Selector: opts.Selector, CWD: repo})
	if err != nil {
		t.Fatalf("merge: %+v: %v", merged, err)
	}
	if !merged.TargetAdvanced || !merged.TargetSynced {
		t.Fatalf("merge %+v", merged)
	}
	if content, err := os.ReadFile(filepath.Join(repo, "feature")); err != nil || string(content) != "change" {
		t.Fatalf("merged file %q %v", content, err)
	}
	current, err := st.LoadManifest(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Checkout.Specification.Kind != model.WorkspaceKindExecutor || current.Checkout.Claim == nil {
		t.Fatal("merge moved the checkout")
	}
	run("printf dirty > dirty")
	if _, err := a.ReleaseWorkItemWorkspace(ctx, opts, false); err == nil {
		t.Fatal("released dirty executor")
	}
	shelved, err := a.Shelve(ctx, opts, false)
	if err != nil {
		t.Fatal(err)
	}
	if shelved.Manifest.Checkout.Claim == nil {
		t.Fatal("shelve destroyed executor")
	}
	run("test -f dirty; rm dirty")
	// Simulate Docker failing after export, before container destruction.
	if profile.Driver == "docker" {
		a.ExecutorTransport = failExecutorRemove{}
		if _, err := a.ReleaseWorkItemWorkspace(ctx, opts, false); err == nil {
			t.Fatal("expected destroy failure")
		}
		pending, err := st.LoadManifest(m.ID)
		if err != nil {
			t.Fatal(err)
		}
		if pending.Checkout.Claim == nil || !pending.Checkout.Claim.Executor.Destroying || pending.Checkout.Claim.Executor.ExportedHead == "" {
			t.Fatalf("lost release recovery state: %+v", pending.Checkout)
		}
		a.ExecutorTransport = nil
	}
	released, err := a.ReleaseWorkItemWorkspace(ctx, opts, false)
	if err != nil {
		t.Fatal(err)
	}
	if released.Manifest.Checkout.Claim != nil {
		t.Fatal("release retained claim")
	}
	again, err := a.EnsureWorkItemWorkspace(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	next, err := a.executorClient(again.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Destroy(context.Background())
	if again.Manifest.Checkout.Claim.Generation != 2 {
		t.Fatal("generation did not advance")
	}
	status, err := next.Status(ctx)
	if err != nil || status.Head != merged.SourceNewSHA {
		t.Fatalf("rematerialized head %+v %v", status, err)
	}
	if profile.Driver == "ssh" && os.Getenv("WI_TEST_DOCKER") == "1" {
		if _, err := a.ReleaseWorkItemWorkspace(ctx, opts, false); err != nil {
			t.Fatal(err)
		}
		a.ExecutorsConfig["test"] = executor.Profile{Driver: "docker", Image: "wi-executor-test"}
		switched, err := a.EnsureWorkItemWorkspace(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		docker, err := a.executorClient(switched.Manifest)
		if err != nil {
			t.Fatal(err)
		}
		defer docker.Destroy(context.Background())
		got, err := docker.Status(ctx)
		if err != nil || got.Head != merged.SourceNewSHA || switched.Manifest.Checkout.Claim.Generation != 3 || switched.Manifest.Checkout.Claim.Path != executor.Workdir {
			t.Fatalf("SSH-to-Docker switch lost history: %+v %v", got, err)
		}
	}
}

// Simulate a transport ending stdin early while Docker itself exits normally.
type truncateExecutorInput struct{ profile executor.Profile }

func (transport truncateExecutorInput) Run(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) error {
	if in != nil {
		data, err := io.ReadAll(in)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte("INCOMPLETE_WRITE_SENTINEL")) {
			data = data[:len(data)/2]
		}
		in = bytes.NewReader(data)
	}
	if transport.profile.Driver == "ssh" {
		return (executor.SSH{Host: transport.profile.Host, Config: transport.profile.SSHConfig}).Run(ctx, args, in, out, stderr)
	}
	return (executor.Docker{}).Run(ctx, args, in, out, stderr)
}

type failExecutorRemove struct{}

func (failExecutorRemove) Run(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "rm" {
		return errors.New("injected destroy failure")
	}
	return (executor.Docker{}).Run(ctx, args, in, out, stderr)
}

func TestDockerPiTools(t *testing.T) {
	if os.Getenv("WI_TEST_DOCKER") != "1" {
		t.Skip("set WI_TEST_DOCKER=1")
	}
	testPiTools(t, executor.Profile{Driver: "docker", Image: "wi-executor-test"})
}
func TestSSHPiTools(t *testing.T) {
	s := executortest.SSHServer(t)
	testPiTools(t, executor.Profile{Driver: "ssh", Host: s.Host, Root: s.Root, SSHConfig: s.Config})
}
func testPiTools(t *testing.T, profile executor.Profile) {
	piPath, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("Pi installation needed for extension integration")
	}
	piPath, err = filepath.EvalSymlinks(piPath)
	if err != nil {
		t.Fatal(err)
	}
	piRoot := filepath.Dir(piPath)
	for {
		if _, err := os.Stat(filepath.Join(piRoot, "dist", "core", "extensions", "loader.js")); err == nil {
			break
		}
		parent := filepath.Dir(piRoot)
		if parent == piRoot {
			t.Fatal("could not locate installed Pi package")
		}
		piRoot = parent
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := executor.Client{Profile: profile, Identity: executor.Identity{Name: fmt.Sprintf("wi-pi-test-%d", time.Now().UnixNano()), Owner: "test", Claim: "pi-tools", Generation: 1}}
	defer c.Destroy(context.Background())
	if err := c.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("/tmp", "wi-pi-executor-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	binary := filepath.Join(root, "wi")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../cmd/wi")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	socket := filepath.Join(root, "tool.sock")
	c.Transport = truncateExecutorInput{profile: profile}
	server, err := workspacetool.ListenStream(socket, executorToolHandler(c, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	extension, err := filepath.Abs("../agentdriver/pi/remoteexec/index.ts")
	if err != nil {
		t.Fatal(err)
	}
	test := exec.CommandContext(ctx, "node", "../executor/testdata/extension-test.mjs")
	test.Env = append(os.Environ(), "WI_TEST_PI_PACKAGE="+piRoot, "WI_TEST_EXECUTOR_EXTENSION="+extension, "WI_WORKSPACE_RUNTIME="+binary, "WI_WORKSPACE_TOOL_SOCKET="+socket, "WI_WORKSPACE_TARGET="+c.Identity.Claim, "WI_WORKSPACE_REMOTE_CWD="+c.Workdir())
	if out, err := test.CombinedOutput(); err != nil {
		t.Fatalf("Pi tools: %s: %v", out, err)
	} else {
		t.Log(string(out))
	}
}

func TestDockerToolStreaming(t *testing.T) {
	if os.Getenv("WI_TEST_DOCKER") != "1" {
		t.Skip("set WI_TEST_DOCKER=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := executor.Client{Profile: executor.Profile{Driver: "docker", Image: "wi-executor-test"}, Identity: executor.Identity{Name: fmt.Sprintf("wi-stream-%d", time.Now().UnixNano()), Owner: "test", Claim: "stream", Generation: 1}}
	defer c.Destroy(context.Background())
	if err := c.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("/tmp", "wi-stream-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	socket := filepath.Join(root, "tool.sock")
	server, err := workspacetool.ListenStream(socket, executorToolHandler(c, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	began := time.Now()
	var first time.Time
	var output bytes.Buffer
	result, err := workspacetool.ExchangeStream(ctx, socket, workspacetool.Request{Operation: "run", JobID: "stream-1", CWD: executor.Workdir, Command: []string{"printf first; sleep 2; printf last; exit 9"}}, func(r workspacetool.Response) error {
		if len(r.Stdout) > 0 && first.IsZero() {
			first = time.Now()
		}
		output.Write(r.Stdout)
		return nil
	})
	if err != nil || result.ExitCode != 9 || output.String() != "firstlast" {
		t.Fatalf("%+v %q %v", result, output.String(), err)
	}
	if first.IsZero() || first.Sub(began) > 1500*time.Millisecond {
		t.Fatalf("output was buffered until completion: %s", first.Sub(began))
	}
}
