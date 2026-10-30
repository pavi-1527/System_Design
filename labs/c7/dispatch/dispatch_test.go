package dispatch

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Every test that could wait on a wrong implementation is bounded by this.
const testTimeout = 10 * time.Second

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

var t0 = time.Date(2026, 3, 2, 2, 0, 0, 0, time.UTC) // 02:00, inside most people's quiet hours

// tClock is a fake clock: time moves only when a test says so.
type tClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(t time.Time) *tClock { return &tClock{t: t} }

func (c *tClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *tClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// tProvider records every call. It returns err, or calls fn when set.
type tProvider struct {
	name string
	mu   sync.Mutex
	call []string // notification IDs, in call order
	err  error
	fn   func(ctx context.Context, n Notification) error
}

func (p *tProvider) Name() string { return p.name }

func (p *tProvider) Send(ctx context.Context, n Notification) error {
	p.mu.Lock()
	p.call = append(p.call, n.ID)
	fn, err := p.fn, p.err
	p.mu.Unlock()
	if fn != nil {
		return fn(ctx, n)
	}
	return err
}

func (p *tProvider) calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.call...)
}

func (p *tProvider) count() int { return len(p.calls()) }

func (p *tProvider) setErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

// tPrefs is a Preferences implementation driven by the test.
type tPrefs struct {
	mu         sync.Mutex
	optOut     map[string]bool // tenant/user/channel
	quietUntil time.Time       // quiet while now < quietUntil
}

func (p *tPrefs) setOptOut(tenant, user string, ch Channel, out bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.optOut == nil {
		p.optOut = map[string]bool{}
	}
	p.optOut[tenant+"/"+user+"/"+string(ch)] = out
}

func (p *tPrefs) OptedOut(tenant, user string, ch Channel) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.optOut[tenant+"/"+user+"/"+string(ch)]
}

func (p *tPrefs) Quiet(tenant, user string, now time.Time) (bool, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.quietUntil.IsZero() && now.Before(p.quietUntil) {
		return true, p.quietUntil
	}
	return false, time.Time{}
}

func tNotif(id string, pr Priority) Notification {
	return Notification{ID: id, TenantID: "cu1", UserID: "m1", Channel: SMS, Priority: pr, Body: "hello"}
}

func mustNew(t *testing.T, cfg Config) *Dispatcher {
	t.Helper()
	d, err := New(cfg)
	if err != nil {
		t.Fatalf("New returned error %v", err)
	}
	if d == nil {
		t.Fatal("New returned a nil *Dispatcher with a nil error")
	}
	return d
}

func mustEnqueue(t *testing.T, d *Dispatcher, ns ...Notification) {
	t.Helper()
	for _, n := range ns {
		if err := d.Enqueue(n); err != nil {
			t.Fatalf("Enqueue(%s): %v", n.ID, err)
		}
	}
}

// process runs one Process pass with a deadline, so a wrong implementation
// fails instead of hanging.
func process(t *testing.T, d *Dispatcher) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- d.Process(ctx) }()
	select {
	case n := <-done:
		return n
	case <-ctx.Done():
		t.Fatalf("Process did not return within %v (a provider call without a timeout, or a notification requeued as due forever?)", testTimeout)
		return 0
	}
}

func outcomes(d *Dispatcher, id string) []Outcome {
	var out []Outcome
	for _, r := range d.Log() {
		if r.Notification.ID == id {
			out = append(out, r.Outcome)
		}
	}
	return out
}

// ---------- config and enqueue ----------

func TestNew_RejectsInvalidConfig(t *testing.T) {
	clk := newClock(t0)
	ok := &tProvider{name: "a"}
	cases := map[string]Config{
		"no clock":                  {Providers: map[Channel][]Provider{SMS: {ok}}},
		"rate limit without window": {Now: clk.Now, RateLimit: RateLimit{Burst: 3}},
		"breaker without cooldown":  {Now: clk.Now, Breaker: BreakerConfig{FailureThreshold: 3}},
		"provider without a name":   {Now: clk.Now, Providers: map[Channel][]Provider{SMS: {&tProvider{}}}},
		"nil provider in the chain": {Now: clk.Now, Providers: map[Channel][]Provider{SMS: {ok, nil}}},
	}
	for name, cfg := range cases {
		if _, err := New(cfg); !errors.Is(err, ErrConfig) {
			t.Errorf("New(%s) = %v, want ErrConfig; a dispatcher built from a bad config fails later, in production, instead of now", name, err)
		}
	}
	d := mustNew(t, Config{Now: clk.Now})
	if d.Pending() != 0 {
		t.Errorf("fresh dispatcher has %d pending notifications", d.Pending())
	}
}

func TestEnqueue_RejectsInvalidAndDuplicateIDs(t *testing.T) {
	clk := newClock(t0)
	a := &tProvider{name: "a"}
	d := mustNew(t, Config{Now: clk.Now, Providers: map[Channel][]Provider{SMS: {a}}})

	bad := []Notification{
		{UserID: "m1", Channel: SMS},                        // no ID
		{ID: "n1", Channel: SMS},                            // no user
		{ID: "n1", UserID: "m1"},                            // no channel
		{ID: "n1", UserID: "m1", Channel: SMS, Priority: 7}, // unknown priority
	}
	for _, n := range bad {
		if err := d.Enqueue(n); !errors.Is(err, ErrInvalid) {
			t.Errorf("Enqueue(%+v) = %v, want ErrInvalid", n, err)
		}
	}

	mustEnqueue(t, d, tNotif("n1", Normal))
	if err := d.Enqueue(tNotif("n1", Normal)); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second Enqueue of n1 = %v, want ErrDuplicate: a retried request would be delivered twice", err)
	}
	if got := process(t, d); got != 1 {
		t.Fatalf("Process handled %d, want 1", got)
	}
	if err := d.Enqueue(tNotif("n1", Normal)); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("Enqueue of n1 after delivery = %v, want ErrDuplicate: the dedupe set must outlive the queue entry", err)
	}
	if a.count() != 1 {
		t.Fatalf("provider called %d times for one ID, want 1", a.count())
	}
}

func TestEnqueue_ConcurrentDuplicatesAreAcceptedOnce(t *testing.T) {
	clk := newClock(t0)
	a := &tProvider{name: "a"}
	d := mustNew(t, Config{Now: clk.Now, Providers: map[Channel][]Provider{SMS: {a}}})

	const callers = 50
	var wg sync.WaitGroup
	var accepted, duplicate int64
	var mu sync.Mutex
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := d.Enqueue(tNotif("otp-77", Urgent)) // the same client retry, 50 times at once
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				accepted++
			case errors.Is(err, ErrDuplicate):
				duplicate++
			default:
				t.Errorf("caller %d: unexpected error %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if accepted != 1 || duplicate != callers-1 {
		t.Fatalf("accepted %d, duplicate %d; want exactly 1 accepted and %d duplicates (check-then-insert must be atomic)", accepted, duplicate, callers-1)
	}
	process(t, d)
	if a.count() != 1 {
		t.Fatalf("provider called %d times, want 1: the member would get %d copies of the same passcode", a.count(), a.count())
	}
}

// ---------- ordering ----------

func TestProcess_UrgentBeforeNormalThenFIFO(t *testing.T) {
	clk := newClock(t0)
	a := &tProvider{name: "a"}
	d := mustNew(t, Config{Now: clk.Now, Providers: map[Channel][]Provider{SMS: {a}, Push: {a}}})

	n := func(id string, pr Priority, ch Channel) Notification {
		x := tNotif(id, pr)
		x.Channel = ch
		x.UserID = "m-" + id
		return x
	}
	mustEnqueue(t, d,
		n("promo-1", Normal, Push), n("promo-2", Normal, SMS), n("promo-3", Normal, Push),
		n("otp-1", Urgent, SMS), n("fraud-1", Urgent, Push))
	if got := process(t, d); got != 5 {
		t.Fatalf("handled %d, want 5", got)
	}
	want := []string{"otp-1", "fraud-1", "promo-1", "promo-2", "promo-3"}
	if got := a.calls(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("delivery order %v, want %v: urgent first, then arrival order", got, want)
	}
}

// ---------- preferences, quiet hours, rate limit ----------

func TestProcess_OptedOutChannelIsRecordedNotSent(t *testing.T) {
	clk := newClock(t0)
	a := &tProvider{name: "a"}
	prefs := &tPrefs{}
	d := mustNew(t, Config{Now: clk.Now, Providers: map[Channel][]Provider{SMS: {a}}, Preferences: prefs})

	mustEnqueue(t, d, tNotif("promo", Normal), tNotif("otp", Urgent))
	prefs.setOptOut("cu1", "m1", SMS, true) // the member opts out while the campaign is queued
	process(t, d)

	if got := a.calls(); len(got) != 1 || got[0] != "otp" {
		t.Fatalf("provider calls %v, want only [otp]: opt-out applies to normal notifications at send time, urgent ones bypass it", got)
	}
	if got := outcomes(d, "promo"); len(got) != 1 || got[0] != OptedOut {
		t.Fatalf("outcomes for promo = %v, want [opted_out]: a dropped notification must leave a record", got)
	}
	var rec Record
	for _, r := range d.Log() {
		if r.Notification.ID == "promo" {
			rec = r
		}
	}
	if rec.Reason == "" {
		t.Error("opted-out record has no Reason; support can't explain why a message was not sent")
	}
	if d.Pending() != 0 {
		t.Errorf("Pending() = %d, want 0: an opted-out notification is dropped, not retried", d.Pending())
	}
}

func TestProcess_QuietHoursDeferNormalAndPassUrgent(t *testing.T) {
	clk := newClock(t0) // 02:00
	a := &tProvider{name: "a"}
	until := t0.Add(6 * time.Hour) // 08:00
	prefs := &tPrefs{quietUntil: until}
	d := mustNew(t, Config{Now: clk.Now, Providers: map[Channel][]Provider{SMS: {a}}, Preferences: prefs})

	mustEnqueue(t, d, tNotif("promo", Normal), tNotif("otp", Urgent))
	process(t, d)
	if got := a.calls(); len(got) != 1 || got[0] != "otp" {
		t.Fatalf("provider calls %v, want [otp]: urgent ignores quiet hours, normal waits", got)
	}
	if d.Pending() != 1 {
		t.Fatalf("Pending() = %d, want 1: a deferred notification stays queued, it is not dropped", d.Pending())
	}
	if due, ok := d.NextDue(); !ok || !due.Equal(until) {
		t.Fatalf("NextDue() = %v, %v; want %v (the end of quiet hours)", due, ok, until)
	}
	if got := outcomes(d, "promo"); len(got) != 1 || got[0] != Deferred {
		t.Fatalf("outcomes for promo = %v, want [deferred]", got)
	}
	for _, r := range d.Log() {
		if r.Notification.ID == "promo" && !r.NextAt.Equal(until) {
			t.Errorf("deferred record NextAt = %v, want %v", r.NextAt, until)
		}
	}

	clk.Advance(3 * time.Hour) // 05:00, still quiet
	if got := process(t, d); got != 0 {
		t.Fatalf("Process at 05:00 handled %d, want 0: not due yet", got)
	}
	clk.Advance(3 * time.Hour) // 08:00
	if got := process(t, d); got != 1 {
		t.Fatalf("Process at 08:00 handled %d, want 1", got)
	}
	if got := a.calls(); len(got) != 2 || got[1] != "promo" {
		t.Fatalf("provider calls %v, want [otp promo]", got)
	}
	if got := outcomes(d, "promo"); len(got) != 2 || got[1] != Delivered {
		t.Fatalf("outcomes for promo = %v, want [deferred delivered]", got)
	}
}

func TestProcess_RateLimitSuppressesBeyondBurstAndRefills(t *testing.T) {
	clk := newClock(t0)
	a := &tProvider{name: "a"}
	d := mustNew(t, Config{
		Now:       clk.Now,
		Providers: map[Channel][]Provider{SMS: {a}},
		RateLimit: RateLimit{Burst: 2, Window: time.Hour},
	})

	mustEnqueue(t, d, tNotif("a1", Normal), tNotif("a2", Normal), tNotif("a3", Normal), tNotif("a4", Normal))
	other := tNotif("b1", Normal)
	other.UserID = "m2"
	mustEnqueue(t, d, other)
	process(t, d)

	if got := a.calls(); strings.Join(got, ",") != "a1,a2,b1" {
		t.Fatalf("provider calls %v, want [a1 a2 b1]: 2 per hour per user, and other users are unaffected", got)
	}
	c := d.Counts()
	if c[Delivered] != 3 || c[Suppressed] != 2 {
		t.Fatalf("counts %v, want 3 delivered and 2 suppressed", c)
	}
	if d.Pending() != 0 {
		t.Fatalf("Pending() = %d, want 0: suppressed notifications are dropped and recorded, not retried", d.Pending())
	}

	clk.Advance(30 * time.Minute) // one token refilled (2 per hour)
	mustEnqueue(t, d, tNotif("a5", Normal), tNotif("a6", Normal))
	process(t, d)
	if got := a.calls(); strings.Join(got, ",") != "a1,a2,b1,a5" {
		t.Fatalf("provider calls %v, want [a1 a2 b1 a5]: 30 minutes refills exactly one token", got)
	}

	mustEnqueue(t, d, tNotif("otp-1", Urgent), tNotif("otp-2", Urgent), tNotif("otp-3", Urgent))
	process(t, d)
	if got := a.count(); got != 7 {
		t.Fatalf("provider calls = %d, want 7: urgent notifications bypass the per-user cap", got)
	}
}

// ---------- providers, failover, timeouts, breakers, retries ----------

func TestProcess_FailsOverToNextProvider(t *testing.T) {
	clk := newClock(t0)
	a := &tProvider{name: "a", err: errors.New("503 service unavailable")}
	b := &tProvider{name: "b"}
	d := mustNew(t, Config{Now: clk.Now, Providers: map[Channel][]Provider{SMS: {a, b}}})

	mustEnqueue(t, d, tNotif("otp", Urgent))
	process(t, d)
	if a.count() != 1 || b.count() != 1 {
		t.Fatalf("calls a=%d b=%d, want 1 and 1: fail over to the next provider in the same pass", a.count(), b.count())
	}
	log := d.Log()
	if len(log) != 1 || log[0].Outcome != Delivered {
		t.Fatalf("log = %+v, want one delivered record", log)
	}
	if n := len(log[0].Attempts); n != 2 {
		t.Fatalf("delivered record has %d attempts, want 2 (the failed one and the successful one)", n)
	}
	if log[0].Attempts[0].Provider != "a" || log[0].Attempts[0].Err == nil || log[0].Attempts[1].Provider != "b" || log[0].Attempts[1].Err != nil {
		t.Fatalf("attempts = %+v; want a failed, then b succeeded", log[0].Attempts)
	}
	if d.Pending() != 0 {
		t.Fatalf("Pending() = %d, want 0", d.Pending())
	}
}

func TestProcess_SendTimeoutCountsAsFailureAndFailsOver(t *testing.T) {
	clk := newClock(t0)
	a := &tProvider{name: "a", fn: func(ctx context.Context, n Notification) error {
		<-ctx.Done() // a vendor that never answers
		return ctx.Err()
	}}
	b := &tProvider{name: "b"}
	d := mustNew(t, Config{Now: clk.Now, Providers: map[Channel][]Provider{SMS: {a, b}}, SendTimeout: 50 * time.Millisecond})

	mustEnqueue(t, d, tNotif("otp", Urgent))
	start := time.Now()
	process(t, d)
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("Process took %v; the per-call timeout must bound a hung provider", el)
	}
	if b.count() != 1 {
		t.Fatalf("b called %d times, want 1: after the timeout the next provider is tried", b.count())
	}
	log := d.Log()
	if len(log) != 1 || len(log[0].Attempts) != 2 {
		t.Fatalf("log = %+v, want one record with two attempts", log)
	}
	if !errors.Is(log[0].Attempts[0].Err, context.DeadlineExceeded) {
		t.Fatalf("first attempt error = %v, want context.DeadlineExceeded", log[0].Attempts[0].Err)
	}
}

func TestProcess_ProviderPanicIsContainedAsFailure(t *testing.T) {
	clk := newClock(t0)
	a := &tProvider{name: "a", fn: func(context.Context, Notification) error { panic("vendor sdk: nil map") }}
	b := &tProvider{name: "b"}
	d := mustNew(t, Config{Now: clk.Now, Providers: map[Channel][]Provider{SMS: {a, b}}})

	mustEnqueue(t, d, tNotif("otp", Urgent))
	process(t, d)
	if b.count() != 1 {
		t.Fatalf("b called %d times, want 1: a panicking provider is a failed attempt, not a dead worker", b.count())
	}
	log := d.Log()
	if len(log) != 1 || len(log[0].Attempts) != 2 || log[0].Attempts[0].Err == nil {
		t.Fatalf("log = %+v, want one delivered record whose first attempt failed", log)
	}
}

func TestBreaker_OpensAfterThresholdAndHalfOpensAfterCooldown(t *testing.T) {
	clk := newClock(t0)
	a := &tProvider{name: "a", err: errors.New("500")}
	b := &tProvider{name: "b"}
	d := mustNew(t, Config{
		Now:       clk.Now,
		Providers: map[Channel][]Provider{SMS: {a, b}},
		Breaker:   BreakerConfig{FailureThreshold: 3, Cooldown: 30 * time.Second},
	})

	for i := 1; i <= 5; i++ {
		mustEnqueue(t, d, tNotif(fmt.Sprintf("n%d", i), Urgent))
	}
	process(t, d)
	if a.count() != 3 || b.count() != 5 {
		t.Fatalf("calls a=%d b=%d, want a=3 (then the breaker opens) and b=5", a.count(), b.count())
	}
	if c := d.Counts(); c[Delivered] != 5 {
		t.Fatalf("counts %v, want 5 delivered", c)
	}

	clk.Advance(29 * time.Second) // still open
	mustEnqueue(t, d, tNotif("n6", Urgent))
	process(t, d)
	if a.count() != 3 {
		t.Fatalf("a called %d times before the cooldown ended, want 3", a.count())
	}

	clk.Advance(time.Second) // cooldown over: one probe goes through, and fails
	mustEnqueue(t, d, tNotif("n7", Urgent))
	process(t, d)
	if a.count() != 4 {
		t.Fatalf("a called %d times after the cooldown, want 4 (exactly one half-open probe)", a.count())
	}
	mustEnqueue(t, d, tNotif("n8", Urgent)) // the failed probe re-opened the breaker
	process(t, d)
	if a.count() != 4 {
		t.Fatalf("a called %d times right after a failed probe, want still 4 (open again for another cooldown)", a.count())
	}

	a.setErr(nil) // the provider recovers
	clk.Advance(30 * time.Second)
	mustEnqueue(t, d, tNotif("n9", Urgent), tNotif("n10", Urgent))
	process(t, d)
	if a.count() != 6 {
		t.Fatalf("a called %d times after recovery, want 6: a successful probe closes the breaker and traffic returns", a.count())
	}
	if c := d.Counts(); c[Delivered] != 10 || c[Failed] != 0 {
		t.Fatalf("counts %v, want 10 delivered and nothing failed: the backup carried the load throughout", c)
	}
}

func TestProcess_ExhaustedAttemptsGoToDeadLettersWithHistory(t *testing.T) {
	clk := newClock(t0)
	a := &tProvider{name: "a", err: errors.New("500")}
	d := mustNew(t, Config{
		Now:         clk.Now,
		Providers:   map[Channel][]Provider{SMS: {a}},
		MaxAttempts: 3,
		Backoff:     func(int) time.Duration { return time.Minute },
	})

	mustEnqueue(t, d, tNotif("alert", Normal))
	if got := process(t, d); got != 1 {
		t.Fatalf("first pass handled %d, want 1", got)
	}
	if a.count() != 1 || d.Pending() != 1 {
		t.Fatalf("after pass 1: calls=%d pending=%d, want 1 and 1 (requeued for a retry)", a.count(), d.Pending())
	}
	if due, ok := d.NextDue(); !ok || !due.Equal(t0.Add(time.Minute)) {
		t.Fatalf("NextDue() = %v, %v; want %v (now + backoff)", due, ok, t0.Add(time.Minute))
	}
	if got := process(t, d); got != 0 || a.count() != 1 {
		t.Fatalf("a second pass at the same instant handled %d (calls=%d); the retry must wait for its backoff", got, a.count())
	}
	clk.Advance(time.Minute)
	process(t, d)
	if a.count() != 2 || d.Pending() != 1 {
		t.Fatalf("after pass 2: calls=%d pending=%d, want 2 and 1", a.count(), d.Pending())
	}
	clk.Advance(time.Minute)
	process(t, d)
	if a.count() != 3 {
		t.Fatalf("after pass 3: calls=%d, want 3", a.count())
	}
	if d.Pending() != 0 {
		t.Fatalf("Pending() = %d after the last attempt, want 0: a dead-lettered notification leaves the queue", d.Pending())
	}
	dead := d.DeadLetters()
	if len(dead) != 1 {
		t.Fatalf("DeadLetters() has %d records, want 1", len(dead))
	}
	r := dead[0]
	if r.Outcome != Failed || r.Notification.ID != "alert" || r.Reason == "" {
		t.Fatalf("dead letter = %+v; want outcome failed for alert, with a reason", r)
	}
	if len(r.Attempts) != 3 {
		t.Fatalf("dead letter has %d attempts, want 3: the DLQ entry must carry the history for support and replay", len(r.Attempts))
	}
	for i, at := range r.Attempts {
		want := t0.Add(time.Duration(i) * time.Minute)
		if at.Provider != "a" || at.Err == nil || !at.At.Equal(want) {
			t.Errorf("attempt %d = %+v; want provider a, an error, at %v", i, at, want)
		}
	}
	if c := d.Counts(); c[Failed] != 1 || c[Delivered] != 0 {
		t.Fatalf("counts %v, want 1 failed and 0 delivered", c)
	}
}

func TestProcess_NoProviderForChannelDeadLettersImmediately(t *testing.T) {
	clk := newClock(t0)
	d := mustNew(t, Config{Now: clk.Now, Providers: map[Channel][]Provider{SMS: {&tProvider{name: "a"}}}})
	n := tNotif("stmt", Normal)
	n.Channel = Email
	mustEnqueue(t, d, n)
	process(t, d)
	dead := d.DeadLetters()
	if len(dead) != 1 || !strings.Contains(dead[0].Reason, "email") {
		t.Fatalf("DeadLetters() = %+v; want one record naming the channel with no provider", dead)
	}
	if d.Pending() != 0 {
		t.Fatalf("Pending() = %d, want 0: retrying can't help when nothing can send it", d.Pending())
	}
}

func TestProcess_RetryWaitsForBreakerCooldownWhenAllProvidersAreOpen(t *testing.T) {
	clk := newClock(t0)
	a := &tProvider{name: "a", err: errors.New("500")}
	d := mustNew(t, Config{
		Now:         clk.Now,
		Providers:   map[Channel][]Provider{SMS: {a}},
		Breaker:     BreakerConfig{FailureThreshold: 2, Cooldown: 10 * time.Minute},
		MaxAttempts: 10,
		Backoff:     func(int) time.Duration { return time.Minute },
	})
	mustEnqueue(t, d, tNotif("alert", Normal))
	process(t, d) // attempt 1 fails
	clk.Advance(time.Minute)
	process(t, d) // attempt 2 fails: breaker opens until t0+11m
	if a.count() != 2 {
		t.Fatalf("calls = %d, want 2", a.count())
	}
	clk.Advance(time.Minute) // t0+2m: retry due, but the only provider is open
	process(t, d)
	if a.count() != 2 {
		t.Fatalf("calls = %d, want still 2: an open breaker means no call", a.count())
	}
	if d.Pending() != 1 {
		t.Fatalf("Pending() = %d, want 1: the notification waits, it is not dead-lettered for a failure that was never attempted", d.Pending())
	}
	if due, ok := d.NextDue(); !ok || !due.Equal(t0.Add(11*time.Minute)) {
		t.Fatalf("NextDue() = %v, %v; want %v, when the breaker may admit a probe", due, ok, t0.Add(11*time.Minute))
	}
	clk.Advance(9 * time.Minute) // t0+11m
	process(t, d)
	if a.count() != 3 {
		t.Fatalf("calls = %d, want 3: the retry doubles as the half-open probe", a.count())
	}
}

func TestDefaultBackoff_GrowsExponentiallyAndCaps(t *testing.T) {
	cases := map[int]time.Duration{0: time.Second, 1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 9: 256 * time.Second, 10: MaxBackoff, 40: MaxBackoff, 1000: MaxBackoff}
	for retry, want := range cases {
		if got := DefaultBackoff(retry); got != want {
			t.Errorf("DefaultBackoff(%d) = %v, want %v", retry, got, want)
		}
	}
}

func TestLog_ReturnsCopies(t *testing.T) {
	clk := newClock(t0)
	a := &tProvider{name: "a"}
	d := mustNew(t, Config{Now: clk.Now, Providers: map[Channel][]Provider{SMS: {a}}})
	mustEnqueue(t, d, tNotif("n1", Normal))
	process(t, d)
	log := d.Log()
	if len(log) != 1 || len(log[0].Attempts) != 1 {
		t.Fatalf("log = %+v, want one record with one attempt", log)
	}
	log[0].Outcome = Failed
	log[0].Attempts[0].Provider = "tampered"
	again := d.Log()
	if again[0].Outcome != Delivered || again[0].Attempts[0].Provider != "a" {
		t.Fatalf("mutating the returned log changed the dispatcher's log: %+v", again[0])
	}
}

// ---------- concurrency ----------

func TestProcess_ConcurrentEnqueueAndProcessDeliverEachOnce(t *testing.T) {
	clk := newClock(t0)
	a := &tProvider{name: "a"}
	d := mustNew(t, Config{Now: clk.Now, Providers: map[Channel][]Provider{SMS: {a}, Push: {a}}})

	const producers, perProducer = 8, 200
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	var producing sync.WaitGroup
	for p := 0; p < producers; p++ {
		producing.Add(1)
		go func(p int) {
			defer producing.Done()
			for i := 0; i < perProducer; i++ {
				n := tNotif(fmt.Sprintf("p%d-%d", p, i), Priority(i%2))
				n.UserID = fmt.Sprintf("m%d", i)
				if i%3 == 0 {
					n.Channel = Push
				}
				if err := d.Enqueue(n); err != nil {
					t.Errorf("Enqueue(%s): %v", n.ID, err)
				}
			}
		}(p)
	}
	done := make(chan struct{})
	go func() { producing.Wait(); close(done) }()

	var processing sync.WaitGroup
	for w := 0; w < 2; w++ {
		processing.Add(1)
		go func() {
			defer processing.Done()
			for ctx.Err() == nil {
				d.Process(ctx)
				select {
				case <-done:
					if d.Pending() == 0 {
						return
					}
				default:
				}
				runtime.Gosched()
			}
		}()
	}
	processing.Wait()
	if ctx.Err() != nil {
		t.Fatalf("timed out after %v with %d pending", testTimeout, d.Pending())
	}

	const want = producers * perProducer
	calls := a.calls()
	if len(calls) != want {
		t.Fatalf("provider called %d times, want %d", len(calls), want)
	}
	seen := map[string]bool{}
	for _, id := range calls {
		if seen[id] {
			t.Fatalf("notification %s delivered more than once", id)
		}
		seen[id] = true
	}
	if c := d.Counts(); c[Delivered] != want {
		t.Fatalf("counts %v, want %d delivered", c, want)
	}
}
