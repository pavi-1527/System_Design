package resilient

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// ErrCircuitOpen is returned, without calling the next layer, while a breaker
// is rejecting requests.
var ErrCircuitOpen = errors.New("resilient: circuit open")

// State is a circuit breaker state.
type State int

const (
	Closed   State = iota // requests flow; failures are counted
	Open                  // requests are rejected until the cooldown ends
	HalfOpen              // a few probe requests are let through to test recovery
)

func (s State) String() string {
	switch s {
	case Closed:
		return "closed"
	case Open:
		return "open"
	case HalfOpen:
		return "half-open"
	}
	return "unknown"
}

// BreakerConfig configures a Breaker.
type BreakerConfig struct {
	// ConsecutiveFailures opens the breaker after this many failures in a row
	// (0 disables this trigger).
	ConsecutiveFailures int
	// Window and FailureRatio open the breaker when, among the last Window
	// outcomes (only once Window outcomes have been seen), at least
	// FailureRatio of them were failures. It is checked each time a failure is
	// recorded. Window 0 disables this trigger.
	Window       int
	FailureRatio float64
	// Cooldown is how long the breaker stays open before allowing probes.
	Cooldown time.Duration
	// HalfOpenProbes (K, default 1) is how many requests are admitted while
	// half-open. The breaker closes when all K succeed, and reopens as soon as
	// one fails; callers beyond K get ErrCircuitOpen.
	HalfOpenProbes int
	// Now is the clock (default time.Now).
	Now func() time.Time
	// OnStateChange is called on every transition, in order, while the breaker
	// holds its lock: it must be fast and must not call back into the breaker.
	OnStateChange func(from, to State)
}

// BreakerStats counts breaker activity.
type BreakerStats struct {
	Rejected int64 // requests refused with ErrCircuitOpen
	Opened   int64 // times the breaker has opened
}

type outcome int

const (
	success outcome = iota
	failure
	ignored // neither: the caller cancelled, or our own bulkhead refused
)

// Breaker is a circuit breaker for one dependency.
//
// What counts as a failure: a transport error or a status >= 500. Statuses
// below 500 (including 429 and all other 4xx) are successes as far as vendor
// health goes. A request cancelled by its caller, or refused by a Bulkhead, is
// ignored entirely.
type Breaker struct {
	cfg BreakerConfig

	mu          sync.Mutex
	state       State
	gen         uint64 // bumped on every state change; stale results are dropped
	consecutive int
	ring        []bool // true = failure
	ringN       int    // outcomes recorded so far, capped at len(ring)
	ringPos     int
	ringFails   int
	openedAt    time.Time
	admitted    int // probes admitted in this half-open phase
	succeeded   int // probes that succeeded in this half-open phase
	stats       BreakerStats
}

// NewBreaker creates a closed breaker.
func NewBreaker(cfg BreakerConfig) *Breaker {
	if cfg.HalfOpenProbes < 1 {
		cfg.HalfOpenProbes = 1
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	b := &Breaker{cfg: cfg}
	if cfg.Window > 0 {
		b.ring = make([]bool, cfg.Window)
	}
	return b
}

// State returns the current state. The Open → HalfOpen transition happens
// lazily, on the first request after the cooldown, so State can still report
// Open after the cooldown has elapsed.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// Stats returns a snapshot of the counters.
func (b *Breaker) Stats() BreakerStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stats
}

func (b *Breaker) setState(to State) {
	from := b.state
	b.state = to
	b.gen++
	b.consecutive, b.ringN, b.ringPos, b.ringFails = 0, 0, 0, 0
	for i := range b.ring {
		b.ring[i] = false
	}
	b.admitted, b.succeeded = 0, 0
	switch to {
	case Open:
		b.openedAt = b.cfg.Now()
		b.stats.Opened++
	}
	if b.cfg.OnStateChange != nil {
		b.cfg.OnStateChange(from, to)
	}
}

// allow admits a request or refuses it. The returned generation identifies the
// state the request was admitted in.
func (b *Breaker) allow() (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == Open && b.cfg.Now().Sub(b.openedAt) >= b.cfg.Cooldown {
		b.setState(HalfOpen)
	}
	switch b.state {
	case Closed:
		return b.gen, nil
	case HalfOpen:
		if b.admitted < b.cfg.HalfOpenProbes {
			b.admitted++
			return b.gen, nil
		}
	}
	b.stats.Rejected++
	return 0, ErrCircuitOpen
}

func (b *Breaker) record(gen uint64, o outcome) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if gen != b.gen {
		return // admitted in an earlier state: its news is stale
	}
	switch b.state {
	case Closed:
		switch o {
		case success:
			b.consecutive = 0
			b.push(false)
		case failure:
			b.consecutive++
			b.push(true)
			if (b.cfg.ConsecutiveFailures > 0 && b.consecutive >= b.cfg.ConsecutiveFailures) ||
				(b.cfg.Window > 0 && b.ringN == b.cfg.Window &&
					float64(b.ringFails) >= b.cfg.FailureRatio*float64(b.cfg.Window)) {
				b.setState(Open)
			}
		}
	case HalfOpen:
		switch o {
		case failure:
			b.setState(Open)
		case success:
			b.succeeded++
			if b.succeeded >= b.cfg.HalfOpenProbes {
				b.setState(Closed)
			}
		case ignored:
			b.admitted-- // give the probe slot back
		}
	}
}

func (b *Breaker) push(fail bool) {
	if b.cfg.Window == 0 {
		return
	}
	if b.ringN == len(b.ring) && b.ring[b.ringPos] {
		b.ringFails--
	}
	b.ring[b.ringPos] = fail
	if fail {
		b.ringFails++
	}
	b.ringPos = (b.ringPos + 1) % len(b.ring)
	if b.ringN < len(b.ring) {
		b.ringN++
	}
}

func classify(req *http.Request, resp *http.Response, err error) outcome {
	switch {
	case err != nil:
		if errors.Is(req.Context().Err(), context.Canceled) || errors.Is(err, ErrBulkheadFull) {
			return ignored
		}
		return failure
	case resp.StatusCode >= 500:
		return failure
	}
	return success
}

// Wrap implements Wrapper.
func (b *Breaker) Wrap(next http.RoundTripper) http.RoundTripper {
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		gen, err := b.allow()
		if err != nil {
			return nil, err
		}
		reported := false
		defer func() {
			if !reported { // next panicked: don't strand a probe slot
				b.record(gen, ignored)
			}
		}()
		resp, err := next.RoundTrip(req)
		reported = true
		b.record(gen, classify(req, resp, err))
		return resp, err
	})
}

// PerHostBreakers keeps one independent Breaker per request URL host, so one
// failing vendor cannot trip the breaker for another.
type PerHostBreakers struct {
	cfg BreakerConfig
	mu  sync.Mutex
	m   map[string]*Breaker
}

// NewPerHost creates per-host breakers that all share cfg.
func NewPerHost(cfg BreakerConfig) *PerHostBreakers {
	return &PerHostBreakers{cfg: cfg, m: map[string]*Breaker{}}
}

func (p *PerHostBreakers) get(host string) *Breaker {
	p.mu.Lock()
	defer p.mu.Unlock()
	b, ok := p.m[host]
	if !ok {
		b = NewBreaker(p.cfg)
		p.m[host] = b
	}
	return b
}

// State reports the state of the breaker for host (Closed if never used).
func (p *PerHostBreakers) State(host string) State { return p.get(host).State() }

// Wrap implements Wrapper.
func (p *PerHostBreakers) Wrap(next http.RoundTripper) http.RoundTripper {
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return p.get(req.URL.Host).Wrap(next).RoundTrip(req)
	})
}
