// Package tenancy is the lab for A9 Unit 1 (§4 tenant isolation, §5 noisy
// neighbours). Two small components that a multi-tenant platform needs on
// every request path:
//
//  1. Store: a tenant-scoped data-access layer. The tenant comes from the
//     context (set once, by trusted middleware, after authentication) and is
//     never a parameter. There is no method that takes a tenant, no method
//     that returns another tenant's rows, and no way to ask "all rows". The
//     isolation comes from the shape of the API, not from every caller
//     remembering a WHERE clause.
//
//  2. Limiter: a per-tenant admission controller with weighted fair sharing
//     and a per-tenant cap (a hierarchical token bucket in the spirit of
//     Linux tc HTB). A noisy neighbour can use idle capacity, but can never
//     take what another tenant is guaranteed, and can never exceed its cap.
//
// Run: go test -race ./labs/a9/tenancy/
//
// # Store behaviour (tests depend on every line)
//
//   - Every method reads the tenant from ctx. No tenant in ctx => ErrNoTenant.
//     A tenant that fails TenantID.Valid (empty, upper case, ':' ...) =>
//     ErrInvalidTenant. Both fail closed: nothing is read or written.
//   - Row IDs are unique per tenant, not globally: tenants "a" and "b" may
//     both own a row "x", and they are different rows.
//   - Insert stores a deep copy, stamps Tenant from ctx and sets Version 1. A
//     row whose Tenant field is non-empty and differs from the context's =>
//     ErrTenantMismatch (someone is trying to write into another tenant).
//     Empty ID => ErrInvalidRow. Existing (tenant, id) => ErrExists.
//   - Get/Update/Delete on an ID that exists only under another tenant behave
//     exactly like a missing ID: ErrNotFound, with an identical error message
//     (no existence oracle).
//   - Every Row handed out is a deep copy: mutating it (including Labels)
//     never changes the stored row.
//   - Update runs mutate on a copy while holding the store's lock (mutate must
//     not call back into the Store), so concurrent read-modify-write cycles
//     are not lost. Whatever mutate does to ID, Tenant or Version is ignored:
//     the stored row keeps its ID and Tenant, and Version increases by 1.
//     A nil mutate => ErrInvalidRow.
//   - List returns the caller's tenant's rows ordered by ID ascending. Filter
//     (optional) is only ever called with that tenant's rows. Limit <= 0 means
//     50 and is capped at 200. After is an exclusive keyset cursor. Next is
//     the ID to pass as After for the next page, or "" when there are no more
//     matching rows.
//   - Count returns the number of rows of the caller's tenant only.
//
// # Limiter behaviour
//
// See LimiterConfig and (*Limiter).Allow.
package tenancy

import (
	"context"
	"errors"
	"math"
	"sort"
	"sync"
	"time"
)

// Errors returned by the Store and Limiter.
var (
	ErrNoTenant       = errors.New("tenancy: no tenant in context")
	ErrInvalidTenant  = errors.New("tenancy: invalid tenant id")
	ErrNotFound       = errors.New("tenancy: not found")
	ErrExists         = errors.New("tenancy: already exists")
	ErrTenantMismatch = errors.New("tenancy: row belongs to a different tenant than the caller")
	ErrInvalidRow     = errors.New("tenancy: invalid row")
	ErrInvalidWeight  = errors.New("tenancy: weight must be a finite number > 0")
)

// TenantID identifies a tenant (a credit union). Provided.
type TenantID string

// Valid reports whether id is 1-63 characters from [a-z0-9_-] starting with a
// letter or digit. ':' and other separators are deliberately invalid so a
// tenant id can never be confused with part of a composite key. Provided.
func (id TenantID) Valid() bool {
	if len(id) == 0 || len(id) > 63 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '_' || c == '-') && i > 0:
		default:
			return false
		}
	}
	return true
}

type ctxKey struct{}

// WithTenant returns a context carrying the tenant. Call it once, in trusted
// middleware, with a tenant derived from the authenticated principal (never
// from a request parameter). Provided.
func WithTenant(ctx context.Context, id TenantID) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext returns the tenant stored by WithTenant. Provided.
func FromContext(ctx context.Context) (TenantID, bool) {
	id, ok := ctx.Value(ctxKey{}).(TenantID)
	return id, ok
}

// Row is a stored record.
type Row struct {
	ID      string
	Tenant  TenantID
	Owner   string
	Data    string
	Labels  map[string]string
	Version int64
}

func (r Row) clone() Row {
	if r.Labels != nil {
		m := make(map[string]string, len(r.Labels))
		for k, v := range r.Labels {
			m[k] = v
		}
		r.Labels = m
	}
	return r
}

// ListOptions control Store.List.
type ListOptions struct {
	Limit  int
	After  string
	Filter func(Row) bool
}

// Page is one page of List results.
type Page struct {
	Rows []Row
	Next string
}

// Store is a tenant-scoped, concurrency-safe in-memory table.
type Store struct {
	mu    sync.Mutex
	parts map[TenantID]map[string]Row // one partition per tenant
}

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{parts: make(map[TenantID]map[string]Row)}
}

// tenantOf extracts and validates the tenant. Every method starts here.
func tenantOf(ctx context.Context) (TenantID, error) {
	t, ok := FromContext(ctx)
	if !ok {
		return "", ErrNoTenant
	}
	if !t.Valid() {
		return "", ErrInvalidTenant
	}
	return t, nil
}

// Insert adds r to the caller's tenant and returns the stored row.
func (s *Store) Insert(ctx context.Context, r Row) (Row, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return Row{}, err
	}
	if r.ID == "" {
		return Row{}, ErrInvalidRow
	}
	if r.Tenant != "" && r.Tenant != t {
		return Row{}, ErrTenantMismatch
	}
	r = r.clone()
	r.Tenant = t
	r.Version = 1
	s.mu.Lock()
	defer s.mu.Unlock()
	part := s.parts[t]
	if part == nil {
		part = make(map[string]Row)
		s.parts[t] = part
	}
	if _, dup := part[r.ID]; dup {
		return Row{}, ErrExists
	}
	part[r.ID] = r
	return r.clone(), nil
}

// Get returns the caller's tenant's row with the given ID.
func (s *Store) Get(ctx context.Context, id string) (Row, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return Row{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.parts[t][id]
	if !ok {
		return Row{}, ErrNotFound
	}
	return r.clone(), nil
}

// Update applies mutate to a copy of the row and stores the result.
func (s *Store) Update(ctx context.Context, id string, mutate func(*Row)) (Row, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return Row{}, err
	}
	if mutate == nil {
		return Row{}, ErrInvalidRow
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.parts[t][id]
	if !ok {
		return Row{}, ErrNotFound
	}
	next := cur.clone()
	mutate(&next)
	next.ID, next.Tenant, next.Version = cur.ID, cur.Tenant, cur.Version+1
	// Store a clone: mutate may have kept the pointer it was given.
	s.parts[t][id] = next.clone()
	return next.clone(), nil
}

// Delete removes the caller's tenant's row.
func (s *Store) Delete(ctx context.Context, id string) error {
	t, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.parts[t][id]; !ok {
		return ErrNotFound
	}
	delete(s.parts[t], id)
	return nil
}

// Count returns how many rows the caller's tenant has.
func (s *Store) Count(ctx context.Context) (int, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.parts[t]), nil
}

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

// List returns one page of the caller's tenant's rows, ordered by ID.
func (s *Store) List(ctx context.Context, opts ListOptions) (Page, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return Page{}, err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultPageSize
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// The caller's partition is the only thing this method can see: there is
	// no "iterate every tenant" code path to forget a predicate on.
	var rows []Row
	if breakNoTenantPredicate {
		// Break-it variant (go test -tags breakit): a "global search" helper
		// someone added that scans every partition and forgot the predicate.
		for _, part := range s.parts {
			for id, r := range part {
				if id > opts.After {
					rows = append(rows, r)
				}
			}
		}
	} else {
		for id, r := range s.parts[t] {
			if id > opts.After {
				rows = append(rows, r)
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ID != rows[j].ID {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].Tenant < rows[j].Tenant
	})
	var out Page
	for _, r := range rows {
		if opts.Filter != nil && !opts.Filter(r.clone()) {
			continue
		}
		if len(out.Rows) == limit {
			out.Next = out.Rows[len(out.Rows)-1].ID
			break
		}
		out.Rows = append(out.Rows, r.clone())
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Limiter
// ---------------------------------------------------------------------------

// eps absorbs floating-point error when comparing token counts to 1.
const eps = 1e-9

// LimiterConfig configures a Limiter.
//
// The model, per tenant i with weight w_i (W = sum of all weights):
//
//   - guaranteed rate g_i = Rate * w_i / W tokens per second, held in an
//     "own" bucket of depth g_i * BurstWindow;
//   - a "cap" bucket refilled at c_i = max(TenantCap, g_i) tokens per second
//     with depth c_i * BurstWindow; every admission costs one cap token;
//   - a shared "spare" pool of depth Rate * BurstWindow. When a tenant's own
//     bucket overflows (it is not using its guarantee), the overflow flows
//     into the spare pool. A tenant whose own bucket is empty may borrow from
//     the spare pool.
//
// Buckets start full except the spare pool, which starts empty.
type LimiterConfig struct {
	Rate          float64       // total tokens/second the platform admits; must be > 0
	TenantCap     float64       // max sustained tokens/second for one tenant; 0 means Rate
	BurstWindow   time.Duration // bucket depth in seconds of traffic; default 1s
	DefaultWeight float64       // weight of tenants never passed to SetWeight; default 1
	Now           func() time.Time
}

// Decision is the outcome of Limiter.Allow.
type Decision struct {
	Allowed    bool
	Borrowed   bool          // admitted using the shared spare pool rather than the tenant's own guarantee
	RetryAfter time.Duration // when denied: earliest time one token will be available from the tenant's own refill; 0 if allowed
	Reason     string        // "" if allowed; else "no-tenant", "over-cap" or "over-share"
}

type tenantBucket struct {
	weight                   float64
	own, cap                 float64
	allowed, denied, borrows int64
}

// Limiter is a per-tenant weighted-fair admission controller. Safe for
// concurrent use.
type Limiter struct {
	mu      sync.Mutex
	cfg     LimiterConfig
	tenants map[TenantID]*tenantBucket
	wsum    float64
	spare   float64
	last    time.Time
}

// NewLimiter applies the defaults documented on LimiterConfig.
func NewLimiter(cfg LimiterConfig) *Limiter {
	if cfg.TenantCap <= 0 {
		cfg.TenantCap = cfg.Rate
	}
	if cfg.BurstWindow <= 0 {
		cfg.BurstWindow = time.Second
	}
	if cfg.DefaultWeight <= 0 {
		cfg.DefaultWeight = 1
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Limiter{cfg: cfg, tenants: make(map[TenantID]*tenantBucket)}
}

func (l *Limiter) window() float64 { return l.cfg.BurstWindow.Seconds() }

func (l *Limiter) guarantee(b *tenantBucket) float64 { return l.cfg.Rate * b.weight / l.wsum }

func (l *Limiter) capRate(b *tenantBucket) float64 {
	return math.Max(l.cfg.TenantCap, l.guarantee(b))
}

// refill brings every bucket up to now. Called with l.mu held, before any
// change in weights and before every admission decision.
func (l *Limiter) refill(now time.Time) {
	if l.last.IsZero() {
		l.last = now
		return
	}
	dt := now.Sub(l.last).Seconds()
	if dt <= 0 {
		return // never run time backwards
	}
	l.last = now
	for _, b := range l.tenants {
		g := l.guarantee(b)
		burst := g * l.window()
		b.own += g * dt
		if b.own > burst {
			l.spare += b.own - burst
			b.own = burst
		}
		c := l.capRate(b)
		b.cap = math.Min(b.cap+c*dt, c*l.window())
	}
	l.spare = math.Min(l.spare, l.cfg.Rate*l.window())
}

// reshape recomputes W and clamps buckets after a weight change.
func (l *Limiter) reshape() {
	l.wsum = 0
	for _, b := range l.tenants {
		l.wsum += b.weight
	}
	for _, b := range l.tenants {
		b.own = math.Min(b.own, l.guarantee(b)*l.window())
		b.cap = math.Min(b.cap, l.capRate(b)*l.window())
	}
}

func (l *Limiter) register(t TenantID, w float64) *tenantBucket {
	l.refill(l.cfg.Now())
	b, ok := l.tenants[t]
	if ok {
		b.weight = w
		l.reshape()
		return b
	}
	b = &tenantBucket{weight: w}
	l.tenants[t] = b
	l.reshape()
	b.own = l.guarantee(b) * l.window()
	b.cap = l.capRate(b) * l.window()
	return b
}

// SetWeight registers tenant t (or changes its weight). Register every tenant
// before traffic starts: a tenant first seen by Allow is registered at
// DefaultWeight, which reshapes everyone's guarantee.
func (l *Limiter) SetWeight(t TenantID, w float64) error {
	if !(w > 0) || math.IsInf(w, 0) {
		return ErrInvalidWeight
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.register(t, w)
	return nil
}

// Allow decides whether the tenant in ctx may proceed (cost: one token).
//
//   - No tenant in ctx (or an invalid one): denied, Reason "no-tenant".
//   - Buckets are refilled from the injected clock (never backwards).
//   - Admitted when the tenant's cap bucket has a token AND either its own
//     bucket has a token (spent first) or the spare pool has one (Borrowed).
//   - Denied with Reason "over-cap" when the cap bucket is empty, otherwise
//     "over-share". RetryAfter is the time until the binding bucket(s) hold
//     one token from their own refill alone (ignoring the spare pool),
//     rounded up to a nanosecond.
func (l *Limiter) Allow(ctx context.Context) Decision {
	t, err := tenantOf(ctx)
	if err != nil {
		return Decision{Reason: "no-tenant"}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.tenants[t]
	if !ok {
		b = l.register(t, l.cfg.DefaultWeight)
	} else {
		l.refill(l.cfg.Now())
	}
	if b.cap >= 1-eps {
		switch {
		case b.own >= 1-eps:
			b.own--
			b.cap--
			b.allowed++
			return Decision{Allowed: true}
		case l.spare >= 1-eps:
			l.spare--
			b.cap--
			b.allowed++
			b.borrows++
			return Decision{Allowed: true, Borrowed: true}
		}
	}
	b.denied++
	d := Decision{Reason: "over-share"}
	var wait float64
	if b.cap < 1-eps {
		d.Reason = "over-cap"
		wait = (1 - b.cap) / l.capRate(b)
	}
	if b.own < 1-eps && l.spare < 1-eps {
		wait = math.Max(wait, (1-b.own)/l.guarantee(b))
	}
	d.RetryAfter = time.Duration(math.Ceil(wait * 1e9))
	return d
}

// Stats reports per-tenant counters.
func (l *Limiter) Stats(t TenantID) (allowed, denied, borrowed int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b, ok := l.tenants[t]; ok {
		return b.allowed, b.denied, b.borrows
	}
	return 0, 0, 0
}
