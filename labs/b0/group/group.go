// Package group is a from-scratch clone of golang.org/x/sync/errgroup.
//
// Lab B0 Unit 2 §2. A Group runs a set of goroutines that together do one
// task: Wait returns when all of them have returned, with the first error;
// WithContext gives them a context that is cancelled as soon as one fails;
// SetLimit bounds how many run at once (build the semaphore yourself from a
// buffered channel: that is the point).
package group

import (
	"context"
	"sync"
)

// Group is a collection of goroutines working on subtasks of one task.
// The zero value is valid: it has no limit and does not cancel anything on
// error. A Group must not be reused for a different task after Wait.
type Group struct {
	// TODO(lab): fields. Suggested: a cancel func(error) (nil for a zero
	// Group), a sync.WaitGroup, a semaphore channel (nil = no limit), and a
	// sync.Once + error to record the first failure.
	wg sync.WaitGroup
}

// WithContext returns a new Group and a context derived from ctx. The
// context is cancelled the first time a function passed to Go returns a
// non-nil error, or the first time Wait returns, whichever happens first.
// context.Cause(ctx) reports that first error.
func WithContext(ctx context.Context) (*Group, context.Context) {
	// TODO(lab): context.WithCancelCause (Go 1.20+).
	return &Group{}, ctx
}

// Go calls f in a new goroutine. If a limit is set, Go blocks until a slot
// is free. The first non-nil error cancels the Group's context and is
// returned by Wait.
func (g *Group) Go(f func() error) {
	// TODO(lab): acquire a slot, wg.Add(1) BEFORE starting the goroutine,
	// record the first error, release the slot when f returns.
	_ = f
}

// TryGo calls f in a new goroutine only if the number of active goroutines
// is below the limit, and reports whether it started f.
func (g *Group) TryGo(f func() error) bool {
	// TODO(lab): a non-blocking acquire (select with default).
	_ = f
	return false
}

// SetLimit limits the number of active goroutines to at most n. A negative
// n means no limit; n == 0 prevents any new goroutine from starting (Go
// would block forever, TryGo returns false). Calling SetLimit while
// goroutines in the group are active is a programming error (errgroup
// panics); you may do the same.
func (g *Group) SetLimit(n int) {
	// TODO(lab)
	_ = n
}

// Wait blocks until every function started with Go or TryGo has returned,
// then returns the first non-nil error (if any). It also cancels the
// context returned by WithContext.
func (g *Group) Wait() error {
	// TODO(lab)
	return nil
}
