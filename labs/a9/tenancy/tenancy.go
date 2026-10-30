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
// Suggested order: Store (Insert/Get, then isolation, then List), then the
// Limiter (single tenant, then guarantees, then borrowing and caps).
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
	"time"
)

var errNotImplemented = errors.New("tenancy: not implemented")

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
	// TODO(lab): a mutex and the rows. Think about the shape of the data
	// structure: if the only way to reach a row is through the caller's
	// tenant's partition, a forgotten predicate has nothing to leak.
}

// NewStore returns an empty Store.
func NewStore() *Store {
	// TODO(lab)
	return &Store{}
}

// Insert adds r to the caller's tenant and returns the stored row.
func (s *Store) Insert(ctx context.Context, r Row) (Row, error) {
	// TODO(lab): tenant from ctx (ErrNoTenant / ErrInvalidTenant), ErrInvalidRow,
	// ErrTenantMismatch, ErrExists; store a deep copy with Tenant and Version 1.
	return Row{}, errNotImplemented
}

// Get returns the caller's tenant's row with the given ID.
func (s *Store) Get(ctx context.Context, id string) (Row, error) {
	// TODO(lab)
	return Row{}, errNotImplemented
}

// Update applies mutate to a copy of the row and stores the result.
// Identity fields (ID, Tenant) are restored after mutate; Version is old+1.
func (s *Store) Update(ctx context.Context, id string, mutate func(*Row)) (Row, error) {
	// TODO(lab): beware of mutate keeping the pointer it was given.
	return Row{}, errNotImplemented
}

// Delete removes the caller's tenant's row.
func (s *Store) Delete(ctx context.Context, id string) error {
	// TODO(lab)
	return errNotImplemented
}

// Count returns how many rows the caller's tenant has.
func (s *Store) Count(ctx context.Context) (int, error) {
	// TODO(lab)
	return 0, errNotImplemented
}

// List returns one page of the caller's tenant's rows, ordered by ID.
//
// Break-it: when breakNoTenantPredicate is true (go test -tags breakit), make
// this method forget the tenant, as a hurried "global search" helper might,
// and watch the isolation tests and the BreakIt_CrossTenantLeak experiment.
func (s *Store) List(ctx context.Context, opts ListOptions) (Page, error) {
	// TODO(lab): fetch Limit+1 matching rows to know whether Next is needed.
	return Page{}, errNotImplemented
}

// ---------------------------------------------------------------------------
// Limiter
// ---------------------------------------------------------------------------

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
// Buckets start full except the spare pool, which starts empty. Refill all
// buckets lazily from the injected clock on every call (no goroutines). Use
// float64 tokens and treat a bucket as holding a token when it has >= 1-1e-9.
// Time never runs backwards: a Now() earlier than the last seen timestamp
// adds nothing and does not move the "last seen" mark.
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

// Limiter is a per-tenant weighted-fair admission controller. Safe for
// concurrent use.
type Limiter struct {
	// TODO(lab): a mutex, per-tenant buckets and weights, the spare pool, the
	// last timestamp.
}

// NewLimiter applies the defaults documented on LimiterConfig.
func NewLimiter(cfg LimiterConfig) *Limiter {
	// TODO(lab)
	return &Limiter{}
}

// SetWeight registers tenant t (or changes its weight); w must be a finite
// number > 0 (else ErrInvalidWeight). Before any weights change, refill every
// bucket up to now under the old weights; afterwards clamp every bucket to
// its new depth. A newly registered tenant starts with full buckets. Register
// every tenant before traffic starts: a tenant first seen by Allow is
// registered at DefaultWeight, which reshapes everyone's guarantee.
func (l *Limiter) SetWeight(t TenantID, w float64) error {
	// TODO(lab)
	return errNotImplemented
}

// Allow decides whether the tenant in ctx may proceed (cost: one token).
//
//   - No tenant in ctx (or an invalid one): denied, Reason "no-tenant".
//   - Buckets are refilled from the injected clock (never backwards). While
//     refilling, whatever a tenant's own bucket cannot hold spills into the
//     spare pool (capped at its depth).
//   - Admitted when the tenant's cap bucket has a token AND either its own
//     bucket has a token (spent first) or the spare pool has one (Borrowed).
//   - Denied with Reason "over-cap" when the cap bucket is empty, otherwise
//     "over-share". RetryAfter is the time until the binding bucket(s) hold
//     one token from their own refill alone (ignoring the spare pool),
//     rounded up to a nanosecond.
func (l *Limiter) Allow(ctx context.Context) Decision {
	// TODO(lab)
	return Decision{}
}

// Stats reports per-tenant counters (decisions made by Allow).
func (l *Limiter) Stats(t TenantID) (allowed, denied, borrowed int64) {
	// TODO(lab)
	return 0, 0, 0
}
