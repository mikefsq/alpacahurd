//go:build !windows

package hurd

import (
	"os"
	"syscall"
)

// canRestartSelf reports whether the setup page may offer to restart alpacahurd.
const canRestartSelf = true

// relaunchSelf starts alpacahurd again after shutdown. Under launchd or systemd it exits
// nonzero, which both restart (KeepAlive SuccessfulExit=false, Restart=on-failure); run by hand,
// it replaces itself with a fresh copy of the same command line.
func relaunchSelf() error {
	if os.Getenv("ALPACA_SYSTEM_SERVICE") == "true" {
		os.Exit(75)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
