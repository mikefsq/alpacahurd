//go:build windows

package hurd

import "errors"

// canRestartSelf is false: the service control manager does not restart a service that exits.
const canRestartSelf = false

func relaunchSelf() error { return errors.New("restart is unavailable on Windows") }
