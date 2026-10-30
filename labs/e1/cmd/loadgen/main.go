// Command loadgen is E1's open-loop load generator (STRETCH milestone 5).
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/chethu007/system-design-course/labs/e1/loadgen"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := loadgen.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
