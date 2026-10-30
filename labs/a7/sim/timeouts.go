//go:build breakit

package main

import (
	"context"
	"flag"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// timeouts: three hops, client → gateway → core. The client gives up after
// 100 ms and retries twice; the gateway calls the core; the core takes 200 ms
// of "work" and checks its context every 5 ms.
//
//	-propagate=false  the gateway gives the core a fresh 400 ms timeout, as a
//	                  handler that builds its own context.Background() does.
//	-propagate=true   the gateway passes the request context down, so the
//	                  core stops when the client leaves.
//
// It counts how many times the core ran its work to completion although
// nobody was waiting for the answer. Millisecond-scale real time: the margins
// are wide (100 ms vs 200 ms), so the counts are stable.

func timeouts(args []string) {
	fs := flag.NewFlagSet("timeouts", flag.ExitOnError)
	propagate := fs.Bool("propagate", false, "pass the caller's context down instead of a fresh timeout")
	actions := fs.Int("n", 40, "user actions")
	_ = fs.Parse(args)

	var coreStarted, coreCompleted, coreAbandoned atomic.Int64

	core := func(ctx context.Context) error {
		coreStarted.Add(1)
		for i := 0; i < 40; i++ { // 40 × 5 ms = 200 ms of work
			select {
			case <-ctx.Done():
				coreAbandoned.Add(1)
				return ctx.Err()
			case <-time.After(5 * time.Millisecond):
			}
		}
		coreCompleted.Add(1)
		return nil
	}

	gateway := func(ctx context.Context) error {
		if *propagate {
			return core(ctx)
		}
		fresh, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		defer cancel()
		return core(fresh)
	}

	var served, gaveUp atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < *actions; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempt := 0; attempt < 3; attempt++ {
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				errc := make(chan error, 1)
				go func() { errc <- gateway(ctx) }()
				select {
				case err := <-errc:
					cancel()
					if err == nil {
						served.Add(1)
						return
					}
				case <-ctx.Done():
					cancel()
				}
			}
			gaveUp.Add(1)
		}()
		time.Sleep(5 * time.Millisecond)
	}
	wg.Wait()
	time.Sleep(500 * time.Millisecond) // let orphaned core work finish

	label := "fresh 400 ms timeout at the gateway"
	if *propagate {
		label = "request context propagated to the core"
	}
	fmt.Printf("%s\n", label)
	fmt.Printf("  user actions:                    %d (3 attempts each, 100 ms client timeout)\n", *actions)
	fmt.Printf("  served:                          %d\n", served.Load())
	fmt.Printf("  gave up:                         %d\n", gaveUp.Load())
	fmt.Printf("  core: work started:              %d\n", coreStarted.Load())
	fmt.Printf("  core: ran to completion:         %d   (200 ms each, for a client that had already left)\n", coreCompleted.Load())
	fmt.Printf("  core: abandoned early:           %d\n", coreAbandoned.Load())
}
