//go:build !windows

package hurd

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// onReloadSignal runs fn on every SIGHUP until ctx ends. systemctl reload
// (ExecReload= in the unit) and kill -HUP land here; the orchestrator page's
// reload buttons are the same operation per device.
func onReloadSignal(ctx context.Context, fn func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	go func() {
		defer signal.Stop(ch)
		for {
			select {
			case <-ch:
				fn()
			case <-ctx.Done():
				return
			}
		}
	}()
}
