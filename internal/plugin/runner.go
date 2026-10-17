// Package plugin runs optional external executables on a schedule. Plugins
// are not part of the core: they receive the path of the wi executable and act
// only through its CLI, so enabling or removing one never changes wi's state
// model.
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// MaxRunTime bounds one plugin run, independent of its interval.
	MaxRunTime = 2 * time.Minute
	// StartDelay lets the daemon finish startup before the first run.
	StartDelay  = 15 * time.Second
	maxLogBytes = 1 << 20
)

// Spec is one enabled plugin with its resolved command.
type Spec struct {
	Name         string         `json:"name"`
	Enabled      bool           `json:"enabled"`
	Command      []string       `json:"command"`
	Interval     time.Duration  `json:"interval"`
	Settings     map[string]any `json:"settings,omitempty"`
	RepositoryID string         `json:"repository_id,omitempty"`
	Instructions string         `json:"-"`
	Commands     []string       `json:"commands,omitempty"`
	JobName      string         `json:"job,omitempty"`
}

// Environment returns the plugin process environment: the caller's
// environment without item or terminal context, plus the wi executable path
// and the plugin's settings. The restricted agent endpoint is an authority
// boundary, not disposable item context, and must survive every invocation.
func Environment(base []string, spec Spec, wiPath string) []string {
	env := make([]string, 0, len(base)+3)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "WI_") && key != "WI_AGENT_DAEMON_SOCKET" || key == "TMUX" || key == "TMUX_PANE" {
			continue
		}
		env = append(env, entry)
	}
	return append(env, Variables(spec, wiPath)...)
}

// Variables are what every plugin invocation receives: WI_EXECUTABLE,
// WI_PLUGIN, and WI_PLUGIN_CONFIG with effective registered settings
// as a JSON object.
func Variables(spec Spec, wiPath string) []string {
	settings := spec.Settings
	if settings == nil {
		settings = map[string]any{}
	}
	encoded, _ := json.Marshal(settings)
	return []string{"WI_EXECUTABLE=" + wiPath, "WI_PLUGIN=" + spec.Name, "WI_PLUGIN_CONFIG=" + string(encoded), "WI_PLUGIN_REPOSITORY_ID=" + spec.RepositoryID}
}

// RunOnce runs one plugin invocation to completion, killing its process group
// when it exceeds MaxRunTime or ctx ends.
func RunOnce(ctx context.Context, spec Spec, wiPath string, stdout, stderr io.Writer) error {
	if len(spec.Command) == 0 {
		return errors.New("plugin command is empty")
	}
	runCtx, cancel := context.WithTimeout(ctx, MaxRunTime)
	defer cancel()
	return runProcess(runCtx, spec, wiPath, nil, stdout, stderr)
}

func runProcess(ctx context.Context, spec Spec, wiPath string, args []string, stdout, stderr io.Writer) error {
	return RunCommand(ctx, spec, args, Environment(os.Environ(), spec, wiPath), "", nil, stdout, stderr)
}

// RunCommand shares process cancellation and pipe-drain behavior with jobs,
// while retaining an explicit caller environment, directory and input stream.
func RunCommand(ctx context.Context, spec Spec, args, env []string, dir string, stdin io.Reader, stdout, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, spec.Command[0], append(append([]string(nil), spec.Command[1:]...), args...)...)
	command.Env, command.Dir, command.Stdin = env, dir, stdin
	command.Stdout, command.Stderr = stdout, stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("plugin %s stopped: %w", spec.Name, ctx.Err())
		}
		return err
	}
	return nil
}

// Runner schedules enabled plugins. Runs of one plugin never overlap.
type Runner struct {
	Host   *Host
	WiPath string
	LogDir string
}

// Run blocks until ctx ends.
func (r Runner) Run(ctx context.Context) {
	if r.Host == nil {
		return
	}
	specs, err := r.Host.JobSpecs(ctx, "")
	if err != nil {
		r.logFailure("scheduler", err)
		return
	}
	var group sync.WaitGroup
	limit := make(chan struct{}, 4)
	for _, spec := range specs {
		if !spec.Enabled {
			continue
		}
		group.Add(1)
		go func(spec Spec) {
			defer group.Done()
			timer := time.NewTimer(StartDelay)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}
				select {
				case limit <- struct{}{}:
				case <-ctx.Done():
					return
				}
				r.runJob(ctx, spec)
				<-limit
				timer.Reset(spec.Interval)
			}
		}(spec)
	}
	group.Wait()
}

func (r Runner) runJob(ctx context.Context, scheduled Spec) {
	h := r.Host.Fresh()
	ids := []string{""}
	for _, job := range h.Registry.definitions[scheduled.Name].Jobs {
		if job.Name == scheduled.JobName && job.Repository {
			if h.ListRepositories == nil {
				r.logFailure(scheduled.Name, fmt.Errorf("repository source is unavailable"))
				return
			}
			var err error
			ids, err = h.ListRepositories(ctx)
			if err != nil {
				r.logFailure(scheduled.Name, err)
				return
			}
		}
	}
	for _, id := range ids {
		specs, err := h.JobSpecs(ctx, id)
		if err != nil {
			r.logFailure(scheduled.Name, fmt.Errorf("repository %s settings: %w", id, err))
			continue
		}
		for _, spec := range specs {
			if spec.Name == scheduled.Name && spec.JobName == scheduled.JobName {
				r.runLogged(ctx, spec)
			}
		}
	}
}

func (r Runner) logFailure(name string, err error) {
	if os.MkdirAll(r.LogDir, 0700) != nil {
		return
	}
	file, e := os.OpenFile(LogPath(r.LogDir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return
	}
	defer file.Close()
	fmt.Fprintf(file, "--- %s preparation failed: %v\n", time.Now().UTC().Format(time.RFC3339), err)
}

// LogPath is where scheduled runs of a plugin append their output.
func LogPath(logDir, name string) string {
	return filepath.Join(logDir, name+".log")
}

func (r Runner) runLogged(ctx context.Context, spec Spec) {
	if err := os.MkdirAll(r.LogDir, 0o700); err != nil {
		return
	}
	path := LogPath(r.LogDir, spec.Name)
	if info, err := os.Stat(path); err == nil && info.Size() > maxLogBytes {
		_ = os.Rename(path, path+".1")
	}
	log, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer log.Close()
	started := time.Now()
	fmt.Fprintf(log, "--- %s run started\n", started.Format(time.RFC3339))
	err = RunOnce(ctx, spec, r.WiPath, log, log)
	status := "ok"
	if err != nil {
		status = err.Error()
	}
	fmt.Fprintf(log, "--- %s run finished after %s: %s\n", time.Now().Format(time.RFC3339), time.Since(started).Round(time.Millisecond), status)
}

// MaxSkillBytes bounds the Markdown one plugin adds to the wi skill.
const MaxSkillBytes = 16 << 10

// Skill reads already registered static instructions. It never executes a plugin.
func Skill(ctx context.Context, spec Spec, _ string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(spec.Instructions) > MaxSkillBytes {
		return "", fmt.Errorf("plugin %s instructions exceed %d bytes", spec.Name, MaxSkillBytes)
	}
	return strings.TrimSpace(spec.Instructions), nil
}
