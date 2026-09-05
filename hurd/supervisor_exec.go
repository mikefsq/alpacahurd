package hurd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// runner executes a platform command and returns its combined output.
type runner func(ctx context.Context, name string, args ...string) (string, error)

func execRunner(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		if s != "" {
			return s, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, s)
		}
		return s, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return s, nil
}

// launchCommand builds the alpacahurd -launch command for an instance.
func launchCommand(self, cfgPath, instance string) []string {
	return []string{self, "-launch", instance, "-config", cfgPath}
}

// platformSupervisor selects systemd, launchd, or SCM when available.
// It falls back to noSupervisor.
func platformSupervisor(cfgPath string) Supervisor {
	self, err := os.Executable()
	if err != nil {
		return noSupervisor{}
	}
	switch runtime.GOOS {
	case "linux":
		if _, err := os.Stat("/run/systemd/system"); err != nil {
			return noSupervisor{}
		}
		if _, err := exec.LookPath("systemctl"); err != nil {
			return noSupervisor{}
		}
		return newSystemdSupervisor(execRunner, self, cfgPath)
	case "darwin":
		if _, err := exec.LookPath("launchctl"); err != nil {
			return noSupervisor{}
		}
		return newLaunchdSupervisor(execRunner, self, cfgPath, "/Library/LaunchDaemons", "/Library/Logs/alpacahurd")
	case "windows":
		if _, err := exec.LookPath("sc.exe"); err != nil {
			return noSupervisor{}
		}
		return newSCMSupervisor(execRunner, self, cfgPath, os.Getenv("ProgramData")+`\alpacahurd\logs`)
	}
	return noSupervisor{}
}
