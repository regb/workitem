package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

type LaunchSpec struct {
	Path    string
	Args    []string
	CWD     string
	Env     []string
	LogPath string
}

type Launcher interface {
	Start(spec LaunchSpec) (int, error)
}

type ExecLauncher struct{}

func (ExecLauncher) Start(spec LaunchSpec) (int, error) {
	if spec.Path == "" {
		return 0, fmt.Errorf("agent runtime executable path is required")
	}
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Dir = spec.CWD
	cmd.Env = spec.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		return 0, err
	}
	defer stdin.Close()
	cmd.Stdin = stdin
	var log *os.File
	if spec.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(spec.LogPath), 0o700); err != nil {
			return 0, err
		}
		log, err = os.OpenFile(spec.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return 0, err
		}
		defer log.Close()
		cmd.Stdout = log
		cmd.Stderr = log
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return 0, err
	}
	return pid, nil
}
