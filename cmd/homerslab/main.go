// Command homerslab is HomersLab's one binary. On the primary it runs twice: as the manager, unprivileged, and as the
// agent, as root (packaging/systemd). It is also the CLI.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/ArjunRachithcf/homerslab/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Main(ctx, os.Args[1:], cli.OS())
	stop()
	os.Exit(code)
}
