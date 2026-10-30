package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chethu007/system-design-course/internal/web"
)

func (a *app) cmdServe(args []string) error {
	fs := newFlags("serve")
	port := fs.Int("port", 8080, "port to listen on (always bound to 127.0.0.1)")
	if _, err := splitArgs(fs, args); err != nil {
		return err
	}
	now := time.Now
	if os.Getenv("SD_NOW") != "" {
		pinned := a.now
		now = func() time.Time { return pinned }
	}
	srv := web.New(web.Config{Root: a.c.Root, ProgressPath: a.progPath, Now: now})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return srv.ListenAndServe(ctx, *port, func(url string) {
		a.printf("sd serve: %s (localhost only; Ctrl-C to stop)\n", url)
		a.printf("Progress: %s — shared with the CLI.\n", a.progPath)
	})
}
