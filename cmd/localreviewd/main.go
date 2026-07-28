package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/uhvesta/cmux-localreview/internal/daemon"
)

// localreviewd is the production loopback daemon. The web application is a
// static React build; it does not require a Node/Bun server at runtime.
func main() {
	var port int
	var dataDir string
	var uiDir string
	var parentPID int
	flag.IntVar(&port, "port", 0, "loopback port (0 chooses an available port)")
	flag.StringVar(&dataDir, "data-dir", "", "override CMUX_LOCALREVIEW_DATA_DIR")
	flag.StringVar(&uiDir, "ui-dir", "", "directory containing the built web application")
	flag.IntVar(&parentPID, "parent-pid", 0, "exit when this parent process exits (Electron sidecar mode)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = watchParent(ctx, parentPID, processAlive)
	d, err := daemon.Start(ctx, daemon.Options{Port: port, DataDir: dataDir, UIDir: uiDir})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("cmux-localreview Go daemon listening on 127.0.0.1:%d\n", d.Port())
	<-ctx.Done()
	if err := d.Close(); err != nil {
		log.Printf("daemon shutdown: %v", err)
	}
}
