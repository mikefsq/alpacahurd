//go:build !windows

package hurd

import (
	"os"
	"syscall"
)

// execDriver replaces this process with the driver, preserving its environment and PID.
func execDriver(exe string, args []string) error {
	return syscall.Exec(exe, append([]string{exe}, args...), os.Environ())
}
