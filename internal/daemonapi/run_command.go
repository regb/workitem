package daemonapi

import (
	"context"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

const (
	// commandTimeout and maxCommandTimeout bound how long a command runs.
	commandTimeout    = 5 * time.Minute
	maxCommandTimeout = time.Hour
	// commandOutputTail is how much of a command's output its job keeps.
	commandOutputTail = 64 << 10
	// commandOutputEvery is how often its job's log tail follows the output.
	commandOutputEvery = 250 * time.Millisecond
)

// RunCommand runs shell code in an item's checkout for a person, as a job
// whose log tail is its output and whose result is its exit status.
func (s *Server) RunCommand(ctx context.Context, req *connect.Request[v1.RunCommandRequest]) (*connect.Response[v1.RunCommandResponse], error) {
	r := req.Msg
	if strings.TrimSpace(r.Command) == "" {
		return nil, invalid("command is required")
	}
	if len(r.Command) > 64<<10 {
		return nil, invalid("command is at most 64 KiB")
	}
	timeout := commandTimeout
	if r.TimeoutSeconds > 0 {
		timeout = min(time.Duration(r.TimeoutSeconds)*time.Second, maxCommandTimeout)
	}
	if _, err := s.requireActive(ctx, r.ItemId); err != nil {
		return nil, rpcError(err)
	}
	job := s.startJobKey(ctx, "run_command", "run_command:"+id(), r.ItemId, "", func(ctx context.Context, j *jobRun) error {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		j.phase("running")
		out := &outputTail{job: j}
		code, err := s.App.RunCommand(ctx, resolve(r.ItemId), r.Command, out)
		out.flush()
		if err != nil {
			return err
		}
		if ctx.Err() == context.DeadlineExceeded {
			out.note("stopped after " + timeout.String())
		}
		j.change(func(job *v1.Job) { job.Result = &v1.Job_Command{Command: &v1.CommandResult{ExitCode: int32(code)}} })
		return nil
	})
	return response(&v1.RunCommandResponse{Job: job}, nil)
}

// outputTail keeps the end of a command's output as its job's log tail,
// updating the job at most every commandOutputEvery.
type outputTail struct {
	mu    sync.Mutex
	job   *jobRun
	tail  []byte
	saved time.Time
}

func (o *outputTail) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.tail = append(o.tail, p...)
	if extra := len(o.tail) - commandOutputTail; extra > 0 {
		o.tail = append([]byte(nil), o.tail[extra:]...)
	}
	if time.Since(o.saved) >= commandOutputEvery {
		o.save()
	}
	return len(p), nil
}

func (o *outputTail) save() {
	o.saved = time.Now()
	o.job.logTail(string(o.tail))
}

func (o *outputTail) flush() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.save()
}

// note adds a line of wi's own to the output.
func (o *outputTail) note(line string) {
	_, _ = o.Write([]byte("\n[wi: " + line + "]\n"))
	o.flush()
}
