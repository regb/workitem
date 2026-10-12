package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

func validateSSHProfile(p Profile) error {
	if p.Image != "" || p.Network != "" {
		return fmt.Errorf("SSH profiles cannot select a Docker image or network")
	}
	if p.Host == "" || strings.HasPrefix(p.Host, "-") || strings.ContainsAny(p.Host, " \t\r\n\x00") {
		return fmt.Errorf("SSH host must be one host or SSH config alias")
	}
	if !path.IsAbs(p.Root) || path.Clean(p.Root) != p.Root || p.Root == "/" || strings.ContainsAny(p.Root, "\r\n\x00") {
		return fmt.Errorf("SSH root must be a clean absolute directory other than /")
	}
	// Bound the private tmux Unix socket path, including a generation suffix.
	if len(p.Root) > 45 {
		return fmt.Errorf("SSH root exceeds 45 bytes; use a shorter dedicated root for tmux sockets")
	}
	if p.SSHConfig != "" && !filepath.IsAbs(p.SSHConfig) {
		return fmt.Errorf("ssh_config must be an absolute host-side path")
	}
	return nil
}

// SSH uses normal OpenSSH authentication and known_hosts, but never prompts or
// forwards the agent. Config aliases can select user, port and identity file.
type SSH struct {
	Host, Config string
	TTY          bool
}

func (s SSH) Run(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) error {
	argv := []string{"-oBatchMode=yes", "-oStrictHostKeyChecking=yes", "-oConnectTimeout=10", "-oServerAliveInterval=15", "-oServerAliveCountMax=3", "-oForwardAgent=no", "-oForwardX11=no", "-oClearAllForwardings=yes", "-oPermitLocalCommand=no"}
	if s.Config != "" {
		argv = append(argv, "-F", s.Config)
	}
	if s.TTY {
		argv = append(argv, "-tt")
	} else {
		argv = append(argv, "-T")
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("NUL in SSH command")
		}
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
	}
	argv = append(argv, "--", s.Host, strings.Join(quoted, " "))
	cmd := exec.CommandContext(ctx, "ssh", argv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, stderr
	return cmd.Run()
}

type sshEnvironment struct{ Client }

func (e sshEnvironment) Exists(ctx context.Context) (bool, error) {
	var out, stderr bytes.Buffer
	err := e.transport().Run(ctx, []string{"sh", "-c", `if [ -d "$1" ]; then printf present; elif [ -e "$1" ] || [ -L "$1" ]; then echo 'workspace root is not a directory' >&2; exit 125; fi`, "wi-exists", path.Dir(e.Workdir())}, nil, &out, &stderr)
	if err != nil {
		return false, fmt.Errorf("inspect SSH workspace: %s: %w", stderr.String(), err)
	}
	return out.String() == "present", nil
}
func (e sshEnvironment) Ensure(ctx context.Context) error {
	_, err := e.operation(ctx, "init", nil)
	return err
}
func (e sshEnvironment) Start(ctx context.Context) error {
	exists, err := e.Exists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("SSH workspace is missing; claim retained")
	}
	if _, err := e.operation(ctx, "check-owner", nil); err != nil {
		return err
	}
	return e.Ensure(ctx)
}
func (e sshEnvironment) Destroy(ctx context.Context) error {
	exists, err := e.Exists(ctx)
	if err != nil || !exists {
		return err
	}
	if _, err := e.operation(ctx, "release", nil); err != nil {
		return err
	}
	_, err = e.operation(ctx, "destroy", nil)
	return err
}
