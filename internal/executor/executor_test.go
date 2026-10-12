package executor

import (
	"bytes"
	"context"
	"fmt"
	"github.com/regb/workitem/internal/executor/executortest"
	"os"
	"testing"
	"time"
)

func TestProfile(t *testing.T) {
	for _, p := range []Profile{{}, {Driver: "ssh", Image: "x"}, {Driver: "docker", Image: "-x"}, {Driver: "docker", Image: "x", Network: "host"}} {
		if p.Validate() == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
	if err := (Profile{Driver: "docker", Image: "wi-executor-test"}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDockerJobs(t *testing.T) {
	if os.Getenv("WI_TEST_DOCKER") != "1" {
		t.Skip("set WI_TEST_DOCKER=1 after building testdata/Dockerfile")
	}
	testJobs(t, Profile{Driver: "docker", Image: "wi-executor-test"})
}
func TestSSHJobs(t *testing.T) {
	s := executortest.SSHServer(t)
	testJobs(t, Profile{Driver: "ssh", Host: s.Host, Root: s.Root, SSHConfig: s.Config})
}
func testJobs(t *testing.T, profile Profile) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := Client{Profile: profile, Identity: Identity{Name: fmt.Sprintf("wi-test-%d", time.Now().UnixNano()), Owner: "test", Claim: "test-claim", Generation: 1}}
	t.Cleanup(func() {
		if err := c.Destroy(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err := c.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.StartJob(ctx, "job-1", "runtime-1", c.Workdir(), "printf first; sleep 1; printf second; exit 7", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	// Retrying start must not execute a second command.
	if err := c.StartJob(ctx, "job-1", "runtime-1", c.Workdir(), "touch duplicate", 0); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var offset int64
	for {
		data, err := c.ReadJob(ctx, "job-1", offset)
		if err != nil {
			t.Fatal(err)
		}
		output.Write(data)
		offset += int64(len(data))
		status, err := c.JobStatus(ctx, "job-1")
		if err != nil {
			t.Fatal(err)
		}
		if status.Exit != nil && offset == status.Size {
			if *status.Exit != 7 {
				t.Fatalf("exit %d", *status.Exit)
			}
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if output.String() != "firstsecond" {
		t.Fatalf("output %q", output.String())
	}
	var out, stderr bytes.Buffer
	code, err := c.Exec(ctx, c.Workdir(), []string{"test", "!", "-e", "duplicate"}, nil, &out, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("duplicate: %d %v", code, err)
	}
	if err := c.StartJob(ctx, "job-2", "runtime-1", c.Workdir(), "(sleep 2; touch escaped-child) & wait", 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := c.CancelJob(ctx, "job-2"); err != nil {
		t.Fatal(err)
	}
	s, err := c.JobStatus(ctx, "job-2")
	if err != nil || s.Exit == nil {
		t.Fatalf("cancel: %+v %v", s, err)
	}
	time.Sleep(2200 * time.Millisecond)
	code, err = c.Exec(ctx, c.Workdir(), []string{"test", "!", "-e", "escaped-child"}, nil, &out, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("cancellation left child alive: %d %v", code, err)
	}
	if err := c.StartJob(ctx, "deadline", "runtime-1", c.Workdir(), "sleep 60", time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1200 * time.Millisecond)
	deadline, err := c.JobStatus(ctx, "deadline")
	if err != nil || deadline.Exit == nil || *deadline.Exit != 124 {
		t.Fatalf("timeout: %+v %v", deadline, err)
	}
	if err := c.CancelJob(ctx, "pre-cancelled"); err != nil {
		t.Fatal(err)
	}
	if err := c.StartJob(ctx, "pre-cancelled", "runtime-1", c.Workdir(), "touch should-not-run", 0); err != nil {
		t.Fatal(err)
	}
	code, err = c.Exec(ctx, c.Workdir(), []string{"test", "!", "-e", "should-not-run"}, nil, &out, &stderr)
	if err != nil || code != 0 {
		t.Fatal("late submission executed after cancellation")
	}
	stale := c
	stale.Identity.Generation++
	if _, err := stale.JobStatus(ctx, "job-1"); err == nil {
		t.Fatal("accepted stale claim")
	}
	if err := c.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.StartJob(ctx, "job-3", "runtime-1", c.Workdir(), "true", 0); err == nil {
		t.Fatal("accepted job during release")
	}
}
