package executor

import (
	"context"
	"fmt"
	"path"
)

// environment owns acquisition and removal, not tools or jobs. Docker owns a
// container; SSH owns only one claim's directory on an existing machine.
type environment interface {
	Exists(context.Context) (bool, error)
	Ensure(context.Context) error
	Start(context.Context) error
	Destroy(context.Context) error
}
type dockerEnvironment struct{ Client }

func (e dockerEnvironment) Exists(ctx context.Context) (bool, error) { return e.dockerExists(ctx) }
func (e dockerEnvironment) Ensure(ctx context.Context) error         { return e.dockerEnsure(ctx) }
func (e dockerEnvironment) Start(ctx context.Context) error          { return e.dockerStart(ctx) }
func (e dockerEnvironment) Destroy(ctx context.Context) error        { return e.dockerDestroy(ctx) }

func (c Client) environment() (environment, error) {
	if err := c.Profile.Validate(); err != nil {
		return nil, err
	}
	if !safeID(c.Identity.Name) || !safeID(c.Identity.Claim) || c.Identity.Generation == 0 || c.Identity.Owner == "" {
		return nil, fmt.Errorf("invalid executor identity")
	}
	if c.Profile.Driver == "ssh" {
		if len(c.StateDir()+"/tmux.sock") >= 104 {
			return nil, fmt.Errorf("SSH state path is too long for a tmux socket")
		}
		return sshEnvironment{c}, nil
	}
	return dockerEnvironment{c}, nil
}
func (c Client) Exists(ctx context.Context) (bool, error) {
	e, err := c.environment()
	if err != nil {
		return false, err
	}
	return e.Exists(ctx)
}
func (c Client) Ensure(ctx context.Context) error {
	e, err := c.environment()
	if err != nil {
		return err
	}
	return e.Ensure(ctx)
}
func (c Client) Start(ctx context.Context) error {
	e, err := c.environment()
	if err != nil {
		return err
	}
	return e.Start(ctx)
}
func (c Client) Destroy(ctx context.Context) error {
	e, err := c.environment()
	if err != nil {
		return err
	}
	return e.Destroy(ctx)
}
func (c Client) Workdir() string {
	if c.Profile.Driver == "ssh" {
		return path.Join(c.Profile.Root, c.Identity.Name, "workspace")
	}
	return Workdir
}
func (c Client) StateDir() string {
	if c.Profile.Driver == "ssh" {
		return path.Join(c.Profile.Root, c.Identity.Name, "state")
	}
	return StateDir
}
