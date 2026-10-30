// Command rkv runs one process of the E2 replicated KV store: a node or the
// coordinator. See modules/E2-replicated-kv/spec.md.
//
//	go run ./labs/e2/cmd/rkv node -id n1 -data /tmp/rkv/n1 -addr 127.0.0.1:7001
//	go run ./labs/e2/cmd/rkv node -id n2 -data /tmp/rkv/n2 -addr 127.0.0.1:7002 -role follower -leader http://127.0.0.1:7001
//	go run ./labs/e2/cmd/rkv coord -addr 127.0.0.1:7000 -nodes n1=http://127.0.0.1:7001,n2=http://127.0.0.1:7002
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/chethu007/system-design-course/labs/e2/rkv"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := rkv.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
