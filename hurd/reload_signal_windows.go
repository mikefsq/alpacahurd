//go:build windows

package hurd

import "context"

// onReloadSignal does nothing on Windows, which has no SIGHUP.
func onReloadSignal(context.Context, func()) {}
