package hurd

import (
	"context"
	"errors"
	"io"
)

// A Supervisor runs device binaries and reports on them. The platform provides
// it: systemd on Linux, launchd on macOS, the Windows SCM. Only systemd offers a
// grouping relation, so the orchestrator holds the device list itself and fans
// each call out per instance; the interface is per-instance.
//
// The orchestrator does not own the processes. It asks the supervisor to act on
// them, and each device is a peer under the supervisor, which is what keeps a
// device running when the orchestrator itself is restarted.
//
// Every supervisor launches the same command line, `alpacahurd -launch
// <instance> -config <hurd.json>`, and alpacahurd then resolves the entry's
// driver to its binary and replaces itself with it (see launch.go). The
// supervisor's own unit, plist, or service record therefore never names a
// driver binary or its arguments: a change of "exec" in the device file needs
// no re-install, and the three platforms carry one line of configuration
// each. On Windows the launched alpacahurd stays as the service host, since a
// Windows service has to speak to the SCM, and runs the driver as its child.
//
// The compiled-in layout needs none of this: every device runs in the
// orchestrator's own process. noSupervisor implements the interface for that
// case, so the orchestrator page renders the same on every platform and a
// mixed deployment shows its in-process devices beside its supervised ones.
type Supervisor interface {
	// Name identifies the implementation for the page and logs.
	Name() string
	// Status reports one instance: whether it is installed with the supervisor,
	// enabled to start at boot, and running now, plus the supervisor's own state
	// text and the last error it saw.
	Status(ctx context.Context, instance string) (InstanceStatus, error)
	// Install registers instance with the supervisor; it does not start it.
	// Idempotent.
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

// ErrNoSupervisor is returned by noSupervisor for every action: there is
// nothing to act on, since the device runs inside the orchestrator.
var ErrNoSupervisor = errors.New("this device runs inside alpacahurd; there is no separate process to control")

// noSupervisor is the Supervisor for the compiled-in layout. Every device runs
// in process, so Status reports it as running whenever the orchestrator is, and
// every action is a clean refusal rather than a failure.
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
