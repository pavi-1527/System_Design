// Package group is a from-scratch clone of golang.org/x/sync/errgroup.
//
// Lab B0 Unit 2 §2. A Group runs a set of goroutines that together do one
// task: Wait returns when all of them have returned, with the first error;
// WithContext gives them a context that is cancelled as soon as one fails;
// SetLimit bounds how many run at once (a semaphore built from a buffered
// channel).
package group

import (
	"context"
	"fmt"
	"sync"
)

// Group is a collection of goroutines working on subtasks of one task.
// The zero value is valid: it has no limit and does not cancel anything on
// error. A Group must not be reused for a different task after Wait.
type Group struct {
	cancel func(error) // nil for a zero Group

	wg sync.WaitGroup

	sem chan struct{} // nil = no limit; capacity = limit

	errOnce sync.Once
	err     error
}

// WithContext returns a new Group and a context derived from ctx. The
// context is cancelled the first time a function passed to Go returns a
// non-nil error, or the first time Wait returns, whichever happens first.
// context.Cause(ctx) reports that first error.
func WithContext(ctx context.Context) (*Group, context.Context) {
	ctx, cancel := context.WithCancelCause(ctx)
	return &Group{cancel: cancel}, ctx
}

func (g *Group) done() {
	if g.sem != nil {
		<-g.sem // release the slot
	}
	g.wg.Done()
}

func (g *Group) run(f func() error) {
	defer g.done()
	if err := f(); err != nil {
		g.errOnce.Do(func() {
			g.err = err
			if g.cancel != nil {
				g.cancel(err)
			}
		})
	}
}

// Go calls f in a new goroutine. If a limit is set, Go blocks until a slot
// is free. The first non-nil error cancels the Group's context and is
// returned by Wait.
func (g *Group) Go(f func() error) {
	if g.sem != nil {
		g.sem <- struct{}{} // acquire: blocks while `limit` goroutines are active
	}
	g.wg.Add(1) // Add BEFORE `go`, never inside the goroutine
	go g.run(f)
}

// TryGo calls f in a new goroutine only if the number of active goroutines
// is below the limit, and reports whether it started f.
func (g *Group) TryGo(f func() error) bool {
	if g.sem != nil {
		select {
		case g.sem <- struct{}{}:
		default:
			return false
		}
	}
	g.wg.Add(1)
	go g.run(f)
	return true
}

// SetLimit limits the number of active goroutines to at most n. A negative
// n means no limit; n == 0 prevents any new goroutine from starting (Go
// would block forever, TryGo returns false). SetLimit must not be called
// while goroutines in the group are active; doing so panics, like errgroup.
func (g *Group) SetLimit(n int) {
	if n < 0 {
		g.sem = nil
		return
	}
	if len(g.sem) != 0 {
		panic(fmt.Errorf("group: modify limit while %v goroutines in the group are still active", len(g.sem)))
	}
	g.sem = make(chan struct{}, n)
}

// Wait blocks until every function started with Go or TryGo has returned,
// then returns the first non-nil error (if any). It also cancels the
// context returned by WithContext.
func (g *Group) Wait() error {
	g.wg.Wait()
	if g.cancel != nil {
		g.cancel(g.err) // a nil cause means context.Canceled
	}
	return g.err
}
