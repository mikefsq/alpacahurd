package hurd

import (
	"context"
	"errors"
	"io"
)

// Supervisor controls device processes through the platform service manager.
type Supervisor interface {
	// Name identifies the implementation for the page and logs.
	Name() string
	// Status reports the instance state from the service manager.
	Status(ctx context.Context, instance string) (InstanceStatus, error)
	// Install registers instance without starting it and is safe to repeat.
	Install(ctx context.Context, instance string) error
	// Uninstall removes instance from the supervisor, stopping it first.
	Uninstall(ctx context.Context, instance string) error
	Start(ctx context.Context, instance string) error
	Stop(ctx context.Context, instance string) error
	Restart(ctx context.Context, instance string) error
	// Enable and Disable set whether instance starts at boot.
	Enable(ctx context.Context, instance string) error
	Disable(ctx context.Context, instance string) error
	// Logs writes the last n lines of instance's log to w.
	Logs(ctx context.Context, instance string, n int, w io.Writer) error
}

// InstanceStatus is one supervised device's state.
type InstanceStatus struct {
	Installed bool
	Enabled   bool
	Running   bool
	State     string // the supervisor's own word: "active", "running", "loaded", ...
	LastError string
	PID       int
}

// ErrNoSupervisor indicates that a device has no separate process to control.
var ErrNoSupervisor = errors.New("this device runs inside alpacahurd; there is no separate process to control")

// noSupervisor reports in-process status and rejects process-control actions.
type noSupervisor struct{}

func (noSupervisor) Name() string { return "in-process" }

func (noSupervisor) Status(_ context.Context, _ string) (InstanceStatus, error) {
	return InstanceStatus{Installed: true, Enabled: true, Running: true, State: "in-process"}, nil
}

func (noSupervisor) Install(context.Context, string) error   { return ErrNoSupervisor }
func (noSupervisor) Uninstall(context.Context, string) error { return ErrNoSupervisor }
func (noSupervisor) Start(context.Context, string) error     { return ErrNoSupervisor }
func (noSupervisor) Stop(context.Context, string) error      { return ErrNoSupervisor }
func (noSupervisor) Restart(context.Context, string) error   { return ErrNoSupervisor }
func (noSupervisor) Enable(context.Context, string) error    { return ErrNoSupervisor }
func (noSupervisor) Disable(context.Context, string) error   { return ErrNoSupervisor }
func (noSupervisor) Logs(_ context.Context, _ string, _ int, w io.Writer) error {
	_, err := io.WriteString(w, "logs for an in-process device are the orchestrator's own log\n")
	return err
}
