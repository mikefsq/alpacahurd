//go:build !windows

package hurd

import (
	"os"
	"syscall"
)

// execDriver replaces the process with the driver binary. The environment
// passes through, so systemd's STATE_DIRECTORY and CONFIGURATION_DIRECTORY and
// launchd's ALPACA_* variables reach the driver, and the supervisor keeps
// signalling the same PID: SIGTERM stops it, SIGHUP reloads it.
func execDriver(exe string, args []string) error {
	return syscall.Exec(exe, append([]string{exe}, args...), os.Environ())
}
