// localreviewd-e2e is an acceptance-test-only daemon. It links a deterministic
// Copilot SDK-shaped backend so browser and Electron flows can be validated
// without a credential or network. It is a distinct binary and is never put
// in release archives or selected by the production daemon's flags/env.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/uhvesta/cmux-localreview/internal/askruntime"
	"github.com/uhvesta/cmux-localreview/internal/copilot"
	"github.com/uhvesta/cmux-localreview/internal/daemon"
	"github.com/uhvesta/cmux-localreview/internal/e2ecopilot"
)

func main() {
	var port int
	var dataDir string
	var uiDir string
	var parentPID int
	flag.IntVar(&port, "port", 0, "loopback port (0 chooses an available port)")
	flag.StringVar(&dataDir, "data-dir", "", "override CMUX_LOCALREVIEW_DATA_DIR")
	flag.StringVar(&uiDir, "ui-dir", "", "directory containing the built web application")
	flag.IntVar(&parentPID, "parent-pid", 0, "exit when this E2E Electron parent exits")
	flag.Parse()

	backend := e2ecopilot.NewBackend()
	factory := &daemon.AskRuntimeFactory{
		Source:         e2ecopilot.TokenSource{},
		BaseDirectory:  "e2e-fixture",
		FallbackModels: nil,
		Build: func(context.Context, copilot.ClientConfig) (*askruntime.Runtime, func() error, error) {
			return askruntime.New(backend), func() error { return nil }, nil
		},
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = watchParent(ctx, parentPID)
	d, err := daemon.Start(ctx, daemon.Options{Port: port, DataDir: dataDir, UIDir: uiDir, AskRuntimeFactory: factory})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("cmux-localreview E2E fixture daemon listening on 127.0.0.1:%d\n", d.Port())
	<-ctx.Done()
	if err := d.Close(); err != nil {
		log.Printf("daemon shutdown: %v", err)
	}
}

func watchParent(ctx context.Context, parentPID int) context.Context {
	if parentPID <= 0 || runtime.GOOS == "windows" {
		return ctx
	}
	child, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-child.Done():
				return
			case <-ticker.C:
				if err := syscall.Kill(parentPID, 0); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return child
}
