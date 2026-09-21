// Command agentboard runs the agentboard server and provides the command
// line used by agents, scripts and hooks. See "agentboard help".
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/JiaBao-do/agentboard/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
