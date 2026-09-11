package hurd

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// systemdUnitTemplate is instantiated once per device file.
const systemdUnitTemplate = "alpacahurd-device@"

// systemdSupervisor drives systemd over systemctl and journalctl.
type systemdSupervisor struct {
	run     runner
	self    string
	cfgPath string
}

func newSystemdSupervisor(run runner, self, cfgPath string) *systemdSupervisor {
	return &systemdSupervisor{run: run, self: self, cfgPath: cfgPath}
}

func (s *systemdSupervisor) Name() string { return "systemd" }

// unit returns the systemd unit name for an instance.
func (s *systemdSupervisor) unit(instance string) string {
	return systemdUnitTemplate + instance + ".service"
}

// Status reads instance properties, including those inherited from the template.
func (s *systemdSupervisor) Status(ctx context.Context, instance string) (InstanceStatus, error) {
	out, err := s.run(ctx, "systemctl", "show", s.unit(instance),
		"--property=LoadState,ActiveState,SubState,UnitFileState,MainPID,Result")
	if err != nil {
		return InstanceStatus{}, err
	}
	props := parseKeyValues(out)
	st := InstanceStatus{
		Installed: props["LoadState"] == "loaded",
		Enabled:   props["UnitFileState"] == "enabled",
		Running:   props["ActiveState"] == "active",
		State:     props["ActiveState"],
	}
	if sub := props["SubState"]; sub != "" && sub != st.State {
		st.State += " (" + sub + ")"
	}
	if r := props["Result"]; r != "" && r != "success" {
		st.LastError = r
	}
	st.PID, _ = strconv.Atoi(props["MainPID"])
	return st, nil
}

// Install reloads systemd and verifies the device unit template.
func (s *systemdSupervisor) Install(ctx context.Context, instance string) error {
	out, err := s.run(ctx, "systemctl", "cat", systemdUnitTemplate+".service")
	if err != nil {
		return fmt.Errorf("the template unit %s.service is not installed (deploy/install.sh installs it): %w", systemdUnitTemplate, err)
	}
	if !strings.Contains(out, "-launch") {
		return fmt.Errorf("the installed %s.service does not run alpacahurd -launch; reinstall it from deploy/", systemdUnitTemplate)
	}
	_, err = s.run(ctx, "systemctl", "daemon-reload")
	return err
}

func (s *systemdSupervisor) Uninstall(ctx context.Context, instance string) error {
	_, err := s.run(ctx, "systemctl", "disable", "--now", s.unit(instance))
	return err
}

func (s *systemdSupervisor) Start(ctx context.Context, instance string) error {
	_, err := s.run(ctx, "systemctl", "start", s.unit(instance))
	return err
}

func (s *systemdSupervisor) Stop(ctx context.Context, instance string) error {
	_, err := s.run(ctx, "systemctl", "stop", s.unit(instance))
	return err
}

func (s *systemdSupervisor) Restart(ctx context.Context, instance string) error {
	_, err := s.run(ctx, "systemctl", "restart", s.unit(instance))
	return err
}

func (s *systemdSupervisor) Enable(ctx context.Context, instance string) error {
	_, err := s.run(ctx, "systemctl", "enable", s.unit(instance))
	return err
}

func (s *systemdSupervisor) Disable(ctx context.Context, instance string) error {
	_, err := s.run(ctx, "systemctl", "disable", s.unit(instance))
	return err
}

func (s *systemdSupervisor) Logs(ctx context.Context, instance string, n int, w io.Writer) error {
	out, err := s.run(ctx, "journalctl", "-u", s.unit(instance), "-n", strconv.Itoa(n), "--no-pager", "-o", "short-iso")
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, out+"\n")
	return err
}

// LogsAll includes the orchestrator and every device service, even stopped or
// removed instances whose journal entries remain. The rest of the host is excluded.
func (s *systemdSupervisor) LogsAll(ctx context.Context, n int, w io.Writer) error {
	out, err := s.run(ctx, "journalctl", "-u", "alpacahurd.service", "-u", systemdUnitTemplate+"*.service", "-n", strconv.Itoa(n), "--no-pager", "-o", "short-iso")
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, out+"\n")
	return err
}

// parseKeyValues reads Key=Value lines, systemctl show's output.
func parseKeyValues(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			m[k] = v
		}
	}
	return m
}
