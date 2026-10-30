// Package lb is a small HTTP load balancer: pluggable picker strategies,
// passive health checking (outlier ejection with cooldown and probation),
// connection draining, and an http.Handler reverse-proxy front end.
//
// It is the lab for A1 Unit 1 (§4–§5). The shape mirrors what Envoy, NGINX
// and HAProxy do internally, minus everything that is not about the
// decisions: pick a backend, track whether it is healthy, stop sending it
// traffic before you take it away.
package lb

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNoHealthyBackends is returned by Pool.Next when every backend is
// ejected or draining. The handler turns it into 503 Service Unavailable.
var ErrNoHealthyBackends = errors.New("lb: no healthy backends")

// ErrUnknownBackend is returned when an operation names a backend that is not
// part of the pool.
var ErrUnknownBackend = errors.New("lb: backend is not in this pool")

// Backend is one upstream server. It tracks how many requests are currently
// in flight to it, which is the signal LeastConnections and
// PowerOfTwoChoices use.
//
// Backend is provided (not part of the exercise).
type Backend struct {
	URL      *url.URL
	inflight atomic.Int64
}

// NewBackend parses rawURL (e.g. "http://10.0.0.7:8080") into a Backend.
func NewBackend(rawURL string) (*Backend, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("lb: backend URL %q needs a scheme and host", rawURL)
	}
	return &Backend{URL: u}, nil
}

// Acquire records the start of a request to this backend.
func (b *Backend) Acquire() { b.inflight.Add(1) }

// Release records the end of a request to this backend.
func (b *Backend) Release() { b.inflight.Add(-1) }

// InFlight reports the number of requests currently in flight.
func (b *Backend) InFlight() int64 { return b.inflight.Load() }

// String returns the backend's host:port.
func (b *Backend) String() string { return b.URL.Host }

// Picker chooses one backend from a non-empty candidate list. Implementations
// must be safe for concurrent use. Pick returns nil only if candidates is
// empty.
type Picker interface {
	Pick(candidates []*Backend) *Backend
}

// RoundRobin cycles through candidates in order. The zero value is ready to
// use. It must be used through a pointer (it holds a counter).
type RoundRobin struct {
	n atomic.Uint64
}

// Pick returns candidates[k % len(candidates)] where k is the number of
// previous calls to Pick on this RoundRobin.
func (r *RoundRobin) Pick(candidates []*Backend) *Backend {
	if len(candidates) == 0 {
		return nil
	}
	k := r.n.Add(1) - 1
	return candidates[k%uint64(len(candidates))]
}

// LeastConnections picks the candidate with the fewest in-flight requests.
// Ties go to the earliest candidate in the slice.
type LeastConnections struct{}

// Pick implements Picker.
func (LeastConnections) Pick(candidates []*Backend) *Backend {
	var best *Backend
	var bestN int64
	for _, b := range candidates {
		n := b.InFlight()
		if best == nil || n < bestN {
			best, bestN = b, n
		}
	}
	return best
}

// PowerOfTwoChoices samples two distinct candidates at random and picks the
// one with fewer in-flight requests ("power of two random choices").
//
// Rand returns a uniformly random int in [0, n). If nil, math/rand/v2.IntN is
// used. Tests inject a deterministic source. Rand must be safe for
// concurrent use if the picker is.
//
// Draw protocol (tests depend on it): with n = len(candidates) >= 2,
// i := Rand(n); j := Rand(n-1); if j >= i { j++ } (so i != j). Return
// candidates[j] if it has strictly fewer in-flight requests than
// candidates[i], else candidates[i]. With one candidate, return it without
// calling Rand.
type PowerOfTwoChoices struct {
	Rand func(n int) int
}

// Pick implements Picker.
func (p PowerOfTwoChoices) Pick(candidates []*Backend) *Backend {
	switch len(candidates) {
	case 0:
		return nil
	case 1:
		return candidates[0]
	}
	rnd := p.Rand
	if rnd == nil {
		rnd = rand.IntN
	}
	n := len(candidates)
	i := rnd(n)
	j := rnd(n - 1)
	if j >= i {
		j++
	}
	if candidates[j].InFlight() < candidates[i].InFlight() {
		return candidates[j]
	}
	return candidates[i]
}

// Config configures a Pool. Zero values get defaults.
type Config struct {
	// Picker chooses among healthy backends. Default: &RoundRobin{}.
	Picker Picker
	// FailureThreshold is the number of consecutive failures after which a
	// backend is ejected. Default: 3.
	FailureThreshold int
	// Cooldown is how long an ejected backend stays out. Default: 10s.
	Cooldown time.Duration
	// Now is the clock. Default: time.Now. Tests inject a fake clock.
	Now func() time.Time
}

type health struct {
	consecutive  int       // consecutive failures since the last success
	ejectedUntil time.Time // zero = not ejected
	probation    bool      // re-admitted after ejection, no success yet
	draining     bool
}

// Pool holds a fixed set of backends and their health state.
//
// Health rules (passive health checking / outlier ejection):
//   - ReportFailure increments the backend's consecutive-failure count; when
//     it reaches FailureThreshold the backend is ejected for Cooldown.
//   - ReportSuccess resets the count to zero (and clears probation).
//   - Reports about a backend that is currently ejected are ignored (late
//     results from requests that were already in flight must not extend or
//     cut short an ejection).
//   - Once Cooldown has elapsed the backend is eligible again but on
//     probation: a single failure ejects it again immediately; a success
//     clears probation.
//   - A draining backend receives no new requests but its health is still
//     tracked.
//
// All methods are safe for concurrent use.
type Pool struct {
	picker    Picker
	threshold int
	cooldown  time.Duration
	now       func() time.Time

	backends []*Backend // fixed order

	mu    sync.Mutex
	state map[*Backend]*health
}

// NewPool creates a pool over backends with cfg (defaults applied).
func NewPool(cfg Config, backends ...*Backend) *Pool {
	p := &Pool{
		picker:    cfg.Picker,
		threshold: cfg.FailureThreshold,
		cooldown:  cfg.Cooldown,
		now:       cfg.Now,
		backends:  append([]*Backend(nil), backends...),
		state:     make(map[*Backend]*health, len(backends)),
	}
	if p.picker == nil {
		p.picker = &RoundRobin{}
	}
	if p.threshold <= 0 {
		p.threshold = 3
	}
	if p.cooldown <= 0 {
		p.cooldown = 10 * time.Second
	}
	if p.now == nil {
		p.now = time.Now
	}
	for _, b := range p.backends {
		p.state[b] = &health{}
	}
	return p
}

// refresh re-admits h (on probation) if its cooldown has elapsed. Caller
// holds p.mu.
func (p *Pool) refresh(h *health, now time.Time) {
	if !h.ejectedUntil.IsZero() && !now.Before(h.ejectedUntil) {
		h.ejectedUntil = time.Time{}
		h.consecutive = 0
		h.probation = true
	}
}

// Healthy returns the backends currently eligible for new requests (not
// ejected, not draining), in the order they were given to NewPool.
func (p *Pool) Healthy() []*Backend {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*Backend, 0, len(p.backends))
	for _, b := range p.backends {
		h := p.state[b]
		p.refresh(h, now)
		if h.ejectedUntil.IsZero() && !h.draining {
			out = append(out, b)
		}
	}
	return out
}

// Next picks a backend for a new request from the healthy set using the
// configured Picker. It returns ErrNoHealthyBackends if none is eligible.
// Next does not call Acquire; the caller does.
func (p *Pool) Next() (*Backend, error) {
	healthy := p.Healthy()
	if len(healthy) == 0 {
		return nil, ErrNoHealthyBackends
	}
	b := p.picker.Pick(healthy)
	if b == nil {
		return nil, ErrNoHealthyBackends
	}
	return b, nil
}

// ReportSuccess records a successful response from b.
func (p *Pool) ReportSuccess(b *Backend) {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	h, ok := p.state[b]
	if !ok {
		return
	}
	p.refresh(h, now)
	if !h.ejectedUntil.IsZero() {
		return
	}
	h.consecutive = 0
	h.probation = false
}

// ReportFailure records a failed request to b (transport error or 5xx).
func (p *Pool) ReportFailure(b *Backend) {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	h, ok := p.state[b]
	if !ok {
		return
	}
	p.refresh(h, now)
	if !h.ejectedUntil.IsZero() {
		return
	}
	h.consecutive++
	if h.probation || h.consecutive >= p.threshold {
		h.ejectedUntil = now.Add(p.cooldown)
		h.consecutive = 0
		h.probation = false
	}
}

// Drain stops new requests going to b and blocks until b has no requests in
// flight (return nil) or ctx is done (return ctx.Err(); b stays draining).
// It returns ErrUnknownBackend if b is not in the pool.
func (p *Pool) Drain(ctx context.Context, b *Backend) error {
	p.mu.Lock()
	h, ok := p.state[b]
	if ok {
		h.draining = true
	}
	p.mu.Unlock()
	if !ok {
		return ErrUnknownBackend
	}
	t := time.NewTicker(2 * time.Millisecond)
	defer t.Stop()
	for {
		if b.InFlight() == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Undrain makes a drained backend eligible for new requests again.
func (p *Pool) Undrain(b *Backend) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if h, ok := p.state[b]; ok {
		h.draining = false
	}
}

// NewHandler returns a reverse proxy that load-balances across p.
//
// For each request it picks a backend with p.Next (503 if none), calls
// Acquire/Release around the proxied call, and reports health:
//   - transport error (connection refused, reset, timeout) => ReportFailure
//     and 502 Bad Gateway;
//   - response status >= 500 => ReportFailure, response passed through;
//   - any other response => ReportSuccess.
//
// A request whose *client* went away (the inbound request's context is done)
// is not the backend's fault and must not be reported as a failure.
//
// transport is used for upstream calls; nil means http.DefaultTransport.
func NewHandler(p *Pool, transport http.RoundTripper) http.Handler {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &proxy{pool: p, transport: transport}
}

type proxy struct {
	pool      *Pool
	transport http.RoundTripper
}

func (h *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, err := h.pool.Next()
	if err != nil {
		http.Error(w, "no healthy backends", http.StatusServiceUnavailable)
		return
	}
	b.Acquire()
	defer b.Release()

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(b.URL)
			pr.SetXForwarded()
		},
		Transport: h.transport,
		ModifyResponse: func(res *http.Response) error {
			if res.StatusCode >= 500 {
				h.pool.ReportFailure(b)
			} else {
				h.pool.ReportSuccess(b)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() != nil {
				// The client gave up; the backend did nothing wrong.
				return
			}
			h.pool.ReportFailure(b)
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}
