// Command shorty runs one E1 process: `shorty store …` or `shorty api …`.
// See modules/E1-url-shortener-service/spec.md for the flags.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/chethu007/system-design-course/solutions/e1/shorty"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := shorty.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
