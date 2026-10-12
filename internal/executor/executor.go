// Package executor runs an item's checkout in Docker or on an existing SSH host.
// The host owns claims; each environment holds a clone, tmux jobs and logs.
package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

const Workdir = "/workspace"
const StateDir = "/wi"
const MaxOutput = 16 << 20

type Profile struct {
	Driver    string `json:"driver"`
	Image     string `json:"image"`
	Network   string `json:"network,omitempty"`
	Host      string `json:"host,omitempty"`
	Root      string `json:"root,omitempty"`
	SSHConfig string `json:"ssh_config,omitempty"`
}

func (p Profile) Validate() error {
	if p.Driver == "ssh" {
		return validateSSHProfile(p)
	}
	if p.Driver != "docker" {
		return fmt.Errorf("executor driver must be docker or ssh")
	}
	if p.Host != "" || p.Root != "" || p.SSHConfig != "" {
		return fmt.Errorf("Docker profiles cannot select SSH settings")
	}
	if p.Image == "" || strings.HasPrefix(p.Image, "-") || strings.ContainsAny(p.Image, " \t\r\n\x00") {
		return fmt.Errorf("executor image is required and must be one image reference")
	}
	if p.Network != "" && p.Network != "none" && p.Network != "bridge" {
		return fmt.Errorf("executor network must be none or bridge")
	}
	return nil
}

type Identity struct {
	Name       string `json:"name"`
	Owner      string `json:"owner"`
	Claim      string `json:"claim"`
	Generation uint64 `json:"generation"`
}

func Name(host, item string, generation uint64) string {
	hash := sha256.Sum256([]byte(host + "\x00" + item))
	return fmt.Sprintf("wi-%s-%d", hex.EncodeToString(hash[:12]), generation)
}

// Transport is deliberately private to Wi's operation implementation, not a
// public driver protocol. Tests substitute a process running the same scripts.
type Transport interface {
	Run(context.Context, []string, io.Reader, io.Writer, io.Writer) error
}

type Docker struct{}

func (Docker) Run(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, stderr
	return cmd.Run()
}

type Client struct {
	Profile   Profile
	Identity  Identity
	Transport Transport
}

func (c Client) transport() Transport {
	if c.Transport != nil {
		return c.Transport
	}
	if c.Profile.Driver == "ssh" {
		return SSH{Host: c.Profile.Host, Config: c.Profile.SSHConfig}
	}
	return Docker{}
}
func (c Client) docker(ctx context.Context, args ...string) ([]byte, error) {
	var out, stderr bytes.Buffer
	err := c.transport().Run(ctx, args, nil, &out, &stderr)
	if err != nil {
		return nil, fmt.Errorf("docker: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return out.Bytes(), nil
}

func (c Client) dockerExists(ctx context.Context) (bool, error) {
	// Listing distinguishes absence from an unavailable daemon; never create on
	// an arbitrary inspection failure.
	data, err := c.docker(ctx, "container", "ls", "-a", "--filter", "name=^/"+c.Identity.Name+"$", "--format", "{{.Names}}")
	return strings.TrimSpace(string(data)) != "", err
}

func (c Client) dockerEnsure(ctx context.Context) error {
	if err := c.Profile.Validate(); err != nil {
		return err
	}
	exists, err := c.Exists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		network := c.Profile.Network
		if network == "" {
			network = "none"
		}
		_, err = c.docker(ctx, "create", "--name", c.Identity.Name, "--label", "wi.owner="+c.Identity.Owner,
			"--label", "wi.claim="+c.Identity.Claim, "--init", "--cap-drop=ALL", "--security-opt=no-new-privileges",
			"--network", network, "--entrypoint", "sh", c.Profile.Image, "-c", "while :; do sleep 3600; done")
		if err != nil {
			return err
		}
	}
	return c.Start(ctx)
}

// Start never replaces a missing executor. A ready claim with a missing container
// requires an explicit release, not silent recreation and loss of dirty work.
func (c Client) dockerStart(ctx context.Context) error {
	if err := c.checkOwner(ctx); err != nil {
		return err
	}
	if _, err := c.docker(ctx, "start", c.Identity.Name); err != nil {
		return err
	}
	_, err := c.operation(ctx, "init", nil)
	return err
}

func (c Client) checkOwner(ctx context.Context) error {
	data, err := c.docker(ctx, "inspect", "--format", `{{index .Config.Labels "wi.owner"}} {{index .Config.Labels "wi.claim"}}`, c.Identity.Name)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(data)) != c.Identity.Owner+" "+c.Identity.Claim {
		return fmt.Errorf("executor owner does not match claim")
	}
	return nil
}

func (c Client) dockerDestroy(ctx context.Context) error {
	exists, err := c.Exists(ctx)
	if err != nil || !exists {
		return err
	}
	if err := c.checkOwner(ctx); err != nil {
		return err
	}
	_, err = c.docker(ctx, "rm", "-f", c.Identity.Name)
	return err
}

func (c Client) operation(ctx context.Context, op string, in io.Reader, args ...string) ([]byte, error) {
	if _, err := c.environment(); err != nil {
		return nil, err
	}
	var out, stderr boundedBuffer
	argv := c.operationArgs(op, args...)
	err := c.transport().Run(ctx, argv, in, &out, &stderr)
	if err != nil {
		return nil, fmt.Errorf("executor %s: %s: %w", op, strings.TrimSpace(string(stderr.Bytes())), err)
	}
	if out.exceeded || stderr.exceeded {
		return nil, fmt.Errorf("executor %s exceeded output limit", op)
	}
	return out.Bytes(), nil
}

func (c Client) operationArgs(op string, args ...string) []string {
	command := append([]string{"sh", "-c", operationScript, "wi-executor", c.Identity.Owner, c.Identity.Claim, fmt.Sprint(c.Identity.Generation), op, c.Workdir(), c.StateDir()}, args...)
	if c.Profile.Driver == "ssh" {
		return command
	}
	return append([]string{"exec", "-i", c.Identity.Name}, command...)
}

type Status struct {
	Branch string
	Head   string
	Dirty  bool
}

func (c Client) Status(ctx context.Context) (Status, error) {
	data, err := c.operation(ctx, "status", nil)
	if err != nil {
		return Status{}, err
	}
	parts := strings.SplitN(string(data), "\n", 3)
	if len(parts) != 3 {
		return Status{}, fmt.Errorf("invalid executor status")
	}
	return Status{Branch: parts[0], Head: parts[1], Dirty: strings.TrimSpace(parts[2]) != ""}, nil
}
func (c Client) Seed(ctx context.Context, bundle io.Reader, branch, name, email string) error {
	_, err := c.operation(ctx, "seed", bundle, branch, name, email)
	return err
}
func (c Client) Update(ctx context.Context, bundle io.Reader) error {
	_, err := c.operation(ctx, "update", bundle)
	return err
}
func (c Client) Export(ctx context.Context, branch string, out io.Writer) error {
	if _, err := c.environment(); err != nil {
		return err
	}
	var stderr boundedBuffer
	err := c.transport().Run(ctx, c.operationArgs("export", branch), nil, out, &stderr)
	if err != nil {
		return fmt.Errorf("export executor Git: %s: %w", stderr.Bytes(), err)
	}
	return nil
}
func (c Client) Release(ctx context.Context) error {
	_, err := c.operation(ctx, "release", nil)
	return err
}
func (c Client) Resume(ctx context.Context) error {
	_, err := c.operation(ctx, "resume", nil)
	return err
}
func (c Client) StopJobs(ctx context.Context) error {
	_, err := c.operation(ctx, "stop-jobs", nil)
	return err
}
func (c Client) Rebase(ctx context.Context, target string) error {
	_, err := c.operation(ctx, "rebase", nil, target)
	return err
}
func (c Client) Reset(ctx context.Context, expected, old string) error {
	_, err := c.operation(ctx, "reset", nil, expected, old)
	return err
}

// Exec is for bounded, short file operations, not long-running Bash commands.
func (c Client) Exec(ctx context.Context, cwd string, command []string, in io.Reader, out, stderr io.Writer) (int, error) {
	if _, err := c.environment(); err != nil {
		return 0, err
	}
	if err := validateCommand(cwd, command); err != nil {
		return 0, err
	}
	err := c.transport().Run(ctx, c.operationArgs("exec", append([]string{cwd}, command...)...), in, out, stderr)
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 0, err
}

func validateCommand(cwd string, command []string) error {
	if len(command) == 0 || len(command) > 256 {
		return fmt.Errorf("invalid executor command")
	}
	size := len(cwd)
	for _, arg := range command {
		size += len(arg)
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("NUL in command")
		}
	}
	if size > 128<<10 {
		return fmt.Errorf("executor command exceeds 128 KiB")
	}
	return nil
}

func (c Client) StartJob(ctx context.Context, id, runtime, cwd, command string, timeout time.Duration) error {
	if !safeID(id) || !safeID(runtime) {
		return fmt.Errorf("invalid job identity")
	}
	if err := validateCommand(cwd, []string{command}); err != nil {
		return err
	}
	if timeout < 0 || timeout > 24*time.Hour {
		return fmt.Errorf("invalid job timeout")
	}
	_, err := c.operation(ctx, "job-start", nil, id, runtime, cwd, command, fmt.Sprint(int64((timeout+time.Second-1)/time.Second)))
	return err
}

type JobStatus struct {
	Size int64 `json:"size"`
	Exit *int  `json:"exit"`
}

func (c Client) JobStatus(ctx context.Context, id string) (JobStatus, error) {
	if !safeID(id) {
		return JobStatus{}, fmt.Errorf("invalid job id")
	}
	data, err := c.operation(ctx, "job-status", nil, id)
	if err != nil {
		return JobStatus{}, err
	}
	var result JobStatus
	err = json.Unmarshal(data, &result)
	return result, err
}
func (c Client) ReadJob(ctx context.Context, id string, offset int64) ([]byte, error) {
	if !safeID(id) || offset < 0 {
		return nil, fmt.Errorf("invalid job cursor")
	}
	return c.operation(ctx, "job-read", nil, id, fmt.Sprint(offset))
}
func (c Client) CancelJob(ctx context.Context, id string) error {
	if !safeID(id) {
		return fmt.Errorf("invalid job id")
	}
	_, err := c.operation(ctx, "job-cancel", nil, id)
	return err
}
func (c Client) Attach(ctx context.Context, in io.Reader, out, stderr io.Writer) error {
	if _, err := c.environment(); err != nil {
		return err
	}
	args := c.operationArgs("terminal")
	if c.Profile.Driver == "ssh" {
		if c.Transport != nil {
			return c.Transport.Run(ctx, args, in, out, stderr)
		}
		return (SSH{Host: c.Profile.Host, Config: c.Profile.SSHConfig, TTY: true}).Run(ctx, args, in, out, stderr)
	}
	args[1] = "-it"
	return c.transport().Run(ctx, args, in, out, stderr)
}
func (c Client) CopyMode(ctx context.Context) error {
	_, err := c.operation(ctx, "copy-mode", nil)
	return err
}
func safeID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

type boundedBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	n := len(data)
	remain := MaxOutput - b.Len()
	if len(data) > remain {
		b.exceeded = true
		data = data[:remain]
	}
	_, _ = b.Buffer.Write(data)
	return n, nil
}
