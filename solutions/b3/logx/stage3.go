package logx

import (
	"context"
	"fmt"
	"time"
)

// FailurePolicy says what an AsyncSink does when WriteBatch fails or panics.
type FailurePolicy struct {
	// MaxRetries is the number of retries after the first attempt (0 = none).
	MaxRetries int
	// Backoff is the delay before the first retry; it doubles for each
	// further retry. Zero means retry immediately.
	Backoff time.Duration
	// Sleep waits for d or until ctx is done (returning ctx.Err()). nil means a
	// real, cancellable sleep. Tests inject a fake. The ctx is cancelled when
	// Close gives up, so a worker never sleeps through a shutdown.
	Sleep func(ctx context.Context, d time.Duration) error
	// OnError is called from the worker goroutine for every failed attempt
	// and once more for each batch that is given up. A panic in OnError is
	// recovered and ignored. nil ignores errors (they are still counted).
	OnError func(ErrorEvent)
}

// ErrorEvent describes one failed WriteBatch attempt.
type ErrorEvent struct {
	Err     error
	Entries int  // size of the batch
	Attempt int  // 1-based
	Final   bool // true if the batch is being given up (Failed += Entries)
}

// PanicError is the error an AsyncSink reports when WriteBatch panics.
type PanicError struct{ Value any }

func (p *PanicError) Error() string { return fmt.Sprintf("logx: sink panicked: %v", p.Value) }

// UnflushedError is returned by Close when entries could not be delivered.
type UnflushedError struct {
	// Remaining is the number of accepted entries that had not been written,
	// dropped or failed when Close gave up (or, on a later call, the number
	// abandoned at the deadline).
	Remaining uint64
	// Err is the context error that ended the wait; nil on a later call.
	Err error
}

func (e *UnflushedError) Error() string {
	s := fmt.Sprintf("logx: %d entries not flushed", e.Remaining)
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *UnflushedError) Unwrap() error { return e.Err }

// NewAsyncWithFailure is NewAsync with a retry/error policy.
func NewAsyncWithFailure(dst BatchSink, cfg AsyncConfig, fp FailurePolicy) (*AsyncSink, error) {
	return newAsync(dst, cfg, fp)
}

// safeWrite calls the destination with its own copy of the batch and turns a
// panic into a *PanicError, so one bad sink cannot kill the worker goroutine.
func (a *AsyncSink) safeWrite(batch []Entry) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &PanicError{Value: r}
		}
	}()
	return a.dst.WriteBatch(append([]Entry(nil), batch...))
}

func (a *AsyncSink) report(ev ErrorEvent) {
	if a.fp.OnError == nil {
		return
	}
	defer func() { _ = recover() }()
	a.fp.OnError(ev)
}

func (a *AsyncSink) sleep(d time.Duration) error {
	if d <= 0 {
		return a.wctx.Err()
	}
	if a.fp.Sleep != nil {
		return a.fp.Sleep(a.wctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-a.wctx.Done():
		return a.wctx.Err()
	}
}

// writeBatch delivers one batch, retrying per the policy, and updates stats.
func (a *AsyncSink) writeBatch(batch []Entry) {
	n := uint64(len(batch))
	attempts := 1 + max(0, a.fp.MaxRetries)
	backoff := a.fp.Backoff
	for attempt := 1; ; attempt++ {
		err := a.safeWrite(batch)
		if err == nil {
			a.mu.Lock()
			a.stats.Written += n
			a.stats.Batches++
			a.mu.Unlock()
			return
		}
		retry := attempt < attempts && a.wctx.Err() == nil
		a.report(ErrorEvent{Err: err, Entries: len(batch), Attempt: attempt, Final: !retry})
		if !retry {
			break
		}
		a.mu.Lock()
		a.stats.Retries++
		a.mu.Unlock()
		if serr := a.sleep(backoff); serr != nil {
			a.report(ErrorEvent{Err: serr, Entries: len(batch), Attempt: attempt, Final: true})
			break
		}
		backoff *= 2
	}
	a.mu.Lock()
	a.stats.Failed += n
	a.mu.Unlock()
}
