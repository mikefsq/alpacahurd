//go:build windows

package hurd

import "context"

// onReloadSignal is a no-op on Windows, which has no SIGHUP; a reload there
// comes through the orchestrator page or a device's setup page.
func onReloadSignal(context.Context, func()) {}
