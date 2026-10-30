// Package lb is a small HTTP load balancer: pluggable picker strategies,
// passive health checking (outlier ejection with cooldown and probation),
// connection draining, and an http.Handler reverse-proxy front end.
//
// It is the lab for A1 Unit 1 (§4–§5). The shape mirrors what Envoy, NGINX
// and HAProxy do internally, minus everything that is not about the
// decisions: pick a backend, track whether it is healthy, stop sending it
// traffic before you take it away.
//
// Suggested order: RoundRobin, LeastConnections, PowerOfTwoChoices (the
// picker tests), then NewPool/Healthy/Next/Report*, then Drain/Undrain, then
// NewHandler. Run: go test -race ./labs/a1/lb/
package lb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"
)

var errNotImplemented = errors.New("lb: not implemented")

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
	// TODO(lab): a counter that is safe for concurrent use (sync/atomic).
}

// Pick returns candidates[k % len(candidates)] where k is the number of
// previous calls to Pick on this RoundRobin.
func (r *RoundRobin) Pick(candidates []*Backend) *Backend {
	// TODO(lab): atomic increment, modulo len(candidates). nil for empty input.
	return nil
}

// LeastConnections picks the candidate with the fewest in-flight requests.
// Ties go to the earliest candidate in the slice.
type LeastConnections struct{}

// Pick implements Picker.
func (LeastConnections) Pick(candidates []*Backend) *Backend {
	// TODO(lab): linear scan on InFlight(). nil for empty input.
	return nil
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
	// TODO(lab): follow the draw protocol in the type comment exactly.
	return nil
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
	// TODO(lab): config (with defaults applied), the backends in their
	// original order, and per-backend health state guarded by a mutex.
	// Hint: re-admission can be computed lazily ("has the cooldown elapsed?")
	// at the start of every method; no background goroutine needed.
}

// NewPool creates a pool over backends with cfg (defaults applied).
func NewPool(cfg Config, backends ...*Backend) *Pool {
	// TODO(lab): apply defaults, copy the backend slice, initialise state.
	return &Pool{}
}

// Healthy returns the backends currently eligible for new requests (not
// ejected, not draining), in the order they were given to NewPool.
func (p *Pool) Healthy() []*Backend {
	// TODO(lab)
	return nil
}

// Next picks a backend for a new request from the healthy set using the
// configured Picker. It returns ErrNoHealthyBackends if none is eligible.
// Next does not call Acquire; the caller does.
func (p *Pool) Next() (*Backend, error) {
	// TODO(lab): Healthy() then Picker.Pick. Do not hold the pool lock while
	// calling the picker.
	return nil, errNotImplemented
}

// ReportSuccess records a successful response from b.
func (p *Pool) ReportSuccess(b *Backend) {
	// TODO(lab)
}

// ReportFailure records a failed request to b (transport error or 5xx).
func (p *Pool) ReportFailure(b *Backend) {
	// TODO(lab)
}

// Drain stops new requests going to b and blocks until b has no requests in
// flight (return nil) or ctx is done (return ctx.Err(); b stays draining).
// It returns ErrUnknownBackend if b is not in the pool.
func (p *Pool) Drain(ctx context.Context, b *Backend) error {
	// TODO(lab): mark draining, then poll InFlight() on a short ticker while
	// selecting on ctx.Done().
	return errNotImplemented
}

// Undrain makes a drained backend eligible for new requests again.
func (p *Pool) Undrain(b *Backend) {
	// TODO(lab)
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
	// TODO(lab): httputil.ReverseProxy with Rewrite (SetURL + SetXForwarded),
	// Transport, ModifyResponse (health on status) and ErrorHandler (health on
	// transport error; check r.Context().Err() first).
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "lb: not implemented", http.StatusNotImplemented)
	})
}
