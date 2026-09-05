package hurd

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// scmServicePrefix names a device's Windows service: alpacahurd-<instance>.
const scmServicePrefix = "alpacahurd-"

// scmSupervisor controls per-instance Windows services through sc.exe.
type scmSupervisor struct {
	run     runner
	self    string
	cfgPath string
	logDir  string
}

func newSCMSupervisor(run runner, self, cfgPath, logDir string) *scmSupervisor {
	return &scmSupervisor{run: run, self: self, cfgPath: cfgPath, logDir: logDir}
}

func (s *scmSupervisor) Name() string { return "Windows SCM" }

func (s *scmSupervisor) service(instance string) string { return scmServicePrefix + instance }
func (s *scmSupervisor) logPath(instance string) string {
	return filepath.Join(s.logDir, instance+".log")
}

// binPath is the service command line, each element quoted for sc.exe.
func (s *scmSupervisor) binPath(instance string) string {
	parts := launchCommand(s.self, s.cfgPath, instance)
	q := make([]string, len(parts))
	for i, p := range parts {
		q[i] = `"` + p + `"`
	}
	return strings.Join(q, " ")
}

// Install creates or updates a service with restart-on-failure settings.
// It does not start the service.
func (s *scmSupervisor) Install(ctx context.Context, instance string) error {
	name := s.service(instance)
	if _, err := s.run(ctx, "sc.exe", "qc", name); err == nil {
		_, err := s.run(ctx, "sc.exe", "config", name, "binPath=", s.binPath(instance))
		return err
	}
	if _, err := s.run(ctx, "sc.exe", "create", name, "binPath=", s.binPath(instance),
		"start=", "auto", "DisplayName=", "alpacahurd device "+instance); err != nil {
		return err
	}
	if _, err := s.run(ctx, "sc.exe", "description", name,
		"ASCOM Alpaca device "+instance+" run by alpacahurd"); err != nil {
		return err
	}
	_, err := s.run(ctx, "sc.exe", "failure", name, "reset=", "60",
		"actions=", "restart/5000/restart/5000/restart/5000")
	return err
}

func (s *scmSupervisor) Uninstall(ctx context.Context, instance string) error {
	_ = s.Stop(ctx, instance)
	_, err := s.run(ctx, "sc.exe", "delete", s.service(instance))
	return err
}

func (s *scmSupervisor) Start(ctx context.Context, instance string) error {
	_, err := s.run(ctx, "sc.exe", "start", s.service(instance))
	if err != nil && strings.Contains(err.Error(), "1056") { // already running
		return nil
	}
	return err
}

func (s *scmSupervisor) Stop(ctx context.Context, instance string) error {
	_, err := s.run(ctx, "sc.exe", "stop", s.service(instance))
	if err != nil && strings.Contains(err.Error(), "1062") { // not started
		return nil
	}
	if err != nil {
		return err
	}
	// Wait for asynchronous Stop to finish before Restart calls Start.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st, err := s.Status(ctx, instance)
		if err != nil || !st.Running && st.State != "STOP_PENDING" {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("%s did not stop within 15 s", s.service(instance))
}

func (s *scmSupervisor) Restart(ctx context.Context, instance string) error {
	if err := s.Stop(ctx, instance); err != nil {
		return err
	}
	return s.Start(ctx, instance)
}

// Enable and Disable select automatic or on-demand startup.
func (s *scmSupervisor) Enable(ctx context.Context, instance string) error {
	_, err := s.run(ctx, "sc.exe", "config", s.service(instance), "start=", "auto")
	return err
}

func (s *scmSupervisor) Disable(ctx context.Context, instance string) error {
	_, err := s.run(ctx, "sc.exe", "config", s.service(instance), "start=", "demand")
	return err
}

// Status reads sc query for the running state and sc qc for the start type.
func (s *scmSupervisor) Status(ctx context.Context, instance string) (InstanceStatus, error) {
	name := s.service(instance)
	qc, err := s.run(ctx, "sc.exe", "qc", name)
	if err != nil {
		if strings.Contains(err.Error(), "1060") { // does not exist
			return InstanceStatus{State: "not installed"}, nil
		}
		return InstanceStatus{}, err
	}
	st := InstanceStatus{Installed: true}
	st.Enabled = strings.Contains(scField(qc, "START_TYPE"), "AUTO_START")
	q, err := s.run(ctx, "sc.exe", "query", name)
	if err != nil {
		return st, err
	}
	state := scField(q, "STATE")
	// "4  RUNNING" -> RUNNING
	if f := strings.Fields(state); len(f) > 0 {
		state = f[len(f)-1]
	}
	st.State = state
	st.Running = state == "RUNNING"
	st.PID, _ = strconv.Atoi(strings.TrimSpace(scField(q, "PID")))
	if code := strings.TrimSpace(scField(q, "WIN32_EXIT_CODE")); code != "" {
		if f := strings.Fields(code); len(f) > 0 && f[0] != "0" {
			st.LastError = "exit code " + code
		}
	}
	return st, nil
}

// Logs tails the service host's log file.
func (s *scmSupervisor) Logs(_ context.Context, instance string, n int, w io.Writer) error {
	return tailFile(s.logPath(instance), n, w)
}

// scField returns the value after "KEY :" in sc.exe's output.
func scField(out, key string) string {
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
