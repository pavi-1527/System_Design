// Command jobq runs one role of the E3 job pipeline (broker, worker or
// provider) on 127.0.0.1. See modules/E3-job-pipeline/spec.md.
//
//	go run ./labs/e3/cmd/jobq broker -addr 127.0.0.1:7300 -data /tmp/jobq
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/chethu007/system-design-course/labs/e3/jobq"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := jobq.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
