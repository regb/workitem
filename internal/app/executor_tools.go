package app

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/regb/workitem/internal/executor"
	"github.com/regb/workitem/internal/workspacetool"
)

func executorToolHandler(c executor.Client, runtime string) workspacetool.StreamHandler {
	return func(ctx context.Context, r workspacetool.Request, emit func(workspacetool.Response) error) (workspacetool.Response, error) {
		if len(r.Stdin) > 16<<20 || len(r.Environment) > 0 || r.TimeoutMS < 0 || r.TimeoutMS > 86400000 {
			return workspacetool.Response{}, fmt.Errorf("unsupported tool input or environment")
		}
		switch r.Operation {
		case "cancel":
			return workspacetool.Response{}, c.CancelJob(ctx, r.JobID)
		case "run":
			if len(r.Command) != 1 {
				return workspacetool.Response{}, fmt.Errorf("job requires one Bash command")
			}
			if err := c.StartJob(ctx, r.JobID, runtime, r.CWD, r.Command[0], time.Duration(r.TimeoutMS)*time.Millisecond); err != nil {
				return workspacetool.Response{}, err
			}
			var offset int64
			for {
				data, err := c.ReadJob(ctx, r.JobID, offset)
				if err != nil {
					return workspacetool.Response{}, err
				}
				if len(data) > 0 {
					if err := emit(workspacetool.Response{Stdout: data}); err != nil {
						return workspacetool.Response{}, err
					}
					offset += int64(len(data))
				}
				status, err := c.JobStatus(ctx, r.JobID)
				if err != nil {
					return workspacetool.Response{}, err
				}
				if status.Exit != nil && offset >= status.Size {
					return workspacetool.Response{ExitCode: *status.Exit}, nil
				}
				if len(data) > 0 {
					continue
				}
				select {
				case <-ctx.Done():
					return workspacetool.Response{}, ctx.Err()
				case <-time.After(100 * time.Millisecond):
				}
			}
		case "":
			out, stderr := &toolBuffer{}, &toolBuffer{}
			code, err := c.Exec(ctx, r.CWD, r.Command, bytes.NewReader(r.Stdin), out, stderr)
			if out.exceeded || stderr.exceeded {
				return workspacetool.Response{}, fmt.Errorf("executor file operation exceeded 16 MiB output limit")
			}
			return workspacetool.Response{Stdout: out.Bytes(), Stderr: stderr.Bytes(), ExitCode: code}, err
		default:
			return workspacetool.Response{}, fmt.Errorf("unsupported executor tool operation")
		}
	}
}

type toolBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *toolBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (16 << 20) - b.Len()
	if n > remaining {
		b.exceeded = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
