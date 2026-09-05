//go:build !windows

package hurd

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// onReloadSignal calls fn on SIGHUP until ctx ends.
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
