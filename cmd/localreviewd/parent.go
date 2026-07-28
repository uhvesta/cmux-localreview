package main

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

// watchParent cancels the daemon context when its explicitly supplied parent
// exits. It is intentionally opt-in: service-managed daemons have no parent,
// while the Electron shell passes its own PID so a crashed shell cannot leave
// an authenticated loopback process running indefinitely.
func watchParent(ctx context.Context, parentPID int, alive func(int) bool) context.Context {
	if parentPID <= 0 {
		return ctx
	}
	child, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if !alive(parentPID) {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return child
}

func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	// EPERM means that the process exists but belongs to another identity.
	return err == nil || errors.Is(err, syscall.EPERM)
}
