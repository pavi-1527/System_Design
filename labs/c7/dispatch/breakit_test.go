//go:build breakit

package dispatch

// Break-it measurements for C7 Unit 2 §5–§6. They never fail; they print
// what happened so you can compare it with your prediction. Run one at a
// time with -v:
//
//	go test -tags breakit -run '^TestBreakIt_DeadProviderWithAndWithoutBreaker$' -v ./labs/c7/dispatch/
//	go test -tags breakit -run '^TestBreakIt_ZeroBackoffRetryStorm$' -v ./labs/c7/dispatch/

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestBreakIt_DeadProviderWithAndWithoutBreaker sends 1,000 urgent
// notifications through a chain whose first provider is dead, once with
// breakers disabled and once with a threshold of 3. Every call to the dead
// provider costs a full SendTimeout in production (here it fails
// instantly, so multiply the count by your timeout to get the wasted
// worker time).
func TestBreakIt_DeadProviderWithAndWithoutBreaker(t *testing.T) {
	for _, threshold := range []int{0, 3} {
		clk := newClock(t0)
		dead := &tProvider{name: "dead", err: errors.New("503")}
		backup := &tProvider{name: "backup"}
		d := mustNew(t, Config{
			Now:       clk.Now,
			Providers: map[Channel][]Provider{SMS: {dead, backup}},
			Breaker:   BreakerConfig{FailureThreshold: threshold, Cooldown: 30 * time.Second},
		})
		for i := 0; i < 1000; i++ {
			mustEnqueue(t, d, tNotif(fmt.Sprintf("otp-%d", i), Urgent))
		}
		d.Process(context.Background())
		t.Logf("threshold=%d: calls to the dead provider = %d, to the backup = %d, delivered = %d (with a 2 s timeout the dead calls would have burned %v of worker time)",
			threshold, dead.count(), backup.count(), d.Counts()[Delivered], time.Duration(dead.count())*2*time.Second)
	}
}

// TestBreakIt_ZeroBackoffRetryStorm removes the backoff: every attempt of
// every notification lands in the same pass, at the same timestamp. In
// production that is the retry wave that hits a recovering vendor.
func TestBreakIt_ZeroBackoffRetryStorm(t *testing.T) {
	clk := newClock(t0)
	flaky := &tProvider{name: "flaky", err: errors.New("500")}
	d := mustNew(t, Config{
		Now:         clk.Now,
		Providers:   map[Channel][]Provider{SMS: {flaky}},
		MaxAttempts: 5,
		Backoff:     func(int) time.Duration { return 0 },
	})
	for i := 0; i < 100; i++ {
		mustEnqueue(t, d, tNotif(fmt.Sprintf("alert-%d", i), Normal))
	}
	handled := d.Process(context.Background())
	dead := d.DeadLetters()
	var first, last time.Time
	for _, r := range dead {
		for _, at := range r.Attempts {
			if first.IsZero() || at.At.Before(first) {
				first = at.At
			}
			if at.At.After(last) {
				last = at.At
			}
		}
	}
	t.Logf("one pass handled %d dequeues, made %d provider calls, dead-lettered %d notifications; all attempts fell between %v and %v (spread: %v)",
		handled, flaky.count(), len(dead), first.Format(time.TimeOnly), last.Format(time.TimeOnly), last.Sub(first))
}
