// Package inventory tracks quantity-based stock (SKUs with a number of units)
// and the temporary holds ("reservations") placed on it. It is the core of a
// shop's checkout, a card-authorisation hold, and a library's lending desk.
//
// Lab B3 Unit 7 §1 (problem 14). Stage 1 is this file; Stage 2 lives in
// stage2.go and Stage 3 in stage3.go. Open those only when the interviewer
// (the unit text) has asked for them.
//
// # Model and invariants
//
// For every SKU, at every instant visible to a caller:
//
//	Total     = units ever stocked (AddSKU + Restock)
//	Committed = units that have left (Commit, Borrow) and not come back (Return)
//	Reserved  = units held by ACTIVE reservations (not expired, committed or released)
//	Available = Total - Committed - Reserved, and Available >= 0 always.
//
// A reservation is active while now < ExpiresAt. Expiry is LAZY: no goroutine
// and no timer. Every operation first discards the reservations whose time has
// come, so an expired hold stops counting the moment anyone looks.
//
// # Lock strategy (reference)
//
// One sync.Mutex guards all state. Every public method takes it once, reads
// the clock once, and holds it to the end, so each call is atomic, multi-item
// reservations cannot deadlock, and the invariants above hold at every unlock.
// Per-SKU locks would not help the case that matters (one hot SKU in a flash
// sale) and would need ordered acquisition for multi-item carts.
package inventory

import (
	"container/heap"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Errors. Compare with errors.Is.
var (
	ErrUnknownSKU   = errors.New("inventory: unknown SKU")
	ErrSKUExists    = errors.New("inventory: SKU already exists")
	ErrInsufficient = errors.New("inventory: insufficient stock")
	ErrInvalidQty   = errors.New("inventory: quantity must be positive")
	ErrInvalidTTL   = errors.New("inventory: ttl must be positive")
	ErrNotFound     = errors.New("inventory: reservation not found")
	ErrExpired      = errors.New("inventory: reservation expired")
	ErrAlreadyDone  = errors.New("inventory: reservation already committed or released")
)

// Line is a quantity of one SKU.
type Line struct {
	SKU string
	Qty int
}

// Reservation is a temporary claim on stock. Lines is normalized: duplicate
// SKUs are summed and the lines are sorted by SKU. The value is a copy; changing
// it does not affect the inventory.
type Reservation struct {
	ID        string
	Lines     []Line
	ExpiresAt time.Time
}

// Stock is a point-in-time view of one SKU.
type Stock struct {
	Total     int
	Committed int
	Reserved  int
	Available int
}

type resvState int

const (
	stActive resvState = iota
	stCommitted
	stReleased
	stExpired
)

type resv struct {
	id      string
	lines   []Line
	expires time.Time
	state   resvState
	ended   time.Time // when it left the active state
	idx     int       // index in the expiry heap; -1 when not in it
	member  string    // set for waitlist holds (Stage 3)
	key     string    // idempotency key that created it, if any (Stage 2)
}

func (r *resv) snapshot() Reservation {
	return Reservation{ID: r.id, Lines: append([]Line(nil), r.lines...), ExpiresAt: r.expires}
}

type sku struct {
	name                       string
	total, committed, reserved int
	waiters                    []*ticket // FIFO (Stage 3)
}

func (s *sku) avail() int { return s.total - s.committed - s.reserved }

type expiryHeap []*resv

func (h expiryHeap) Len() int           { return len(h) }
func (h expiryHeap) Less(i, j int) bool { return h[i].expires.Before(h[j].expires) }
func (h expiryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].idx, h[j].idx = i, j
}
func (h *expiryHeap) Push(x any) { r := x.(*resv); r.idx = len(*h); *h = append(*h, r) }
func (h *expiryHeap) Pop() any {
	old := *h
	r := old[len(old)-1]
	old[len(old)-1] = nil
	*h = old[:len(old)-1]
	r.idx = -1
	return r
}

// Inventory is safe for concurrent use.
type Inventory struct {
	mu   sync.Mutex
	now  func() time.Time
	skus map[string]*sku
	resv map[string]*resv
	exp  expiryHeap
	seq  int

	keys map[string]*keyEntry // Stage 2

	loans   map[string]*loan // Stage 3
	active  map[string]int   // member -> unreturned loans
	limit   int
	tickets map[string]*ticket
}

// New returns an empty inventory reading time from now (time.Now if nil).
func New(now func() time.Time) *Inventory {
	if now == nil {
		now = time.Now
	}
	return &Inventory{
		now:     now,
		skus:    map[string]*sku{},
		resv:    map[string]*resv{},
		keys:    map[string]*keyEntry{},
		loans:   map[string]*loan{},
		active:  map[string]int{},
		tickets: map[string]*ticket{},
	}
}

func (inv *Inventory) nextID(prefix string) string {
	inv.seq++
	return fmt.Sprintf("%s-%d", prefix, inv.seq)
}

// tick reads the clock once, discards reservations that have expired, hands
// freed units to waiters, and returns the time to use for the rest of the call.
// Caller holds inv.mu.
func (inv *Inventory) tick() time.Time {
	now := inv.now()
	var touched []*sku
	for len(inv.exp) > 0 && !inv.exp[0].expires.After(now) {
		r := heap.Pop(&inv.exp).(*resv)
		r.state, r.ended = stExpired, r.expires
		for _, l := range r.lines {
			s := inv.skus[l.SKU]
			s.reserved -= l.Qty
			touched = append(touched, s)
		}
	}
	for _, s := range touched {
		inv.serve(s, now)
	}
	return now
}

// AddSKU registers a SKU with total units. total may be zero.
func (inv *Inventory) AddSKU(name string, total int) error {
	if total < 0 {
		return ErrInvalidQty
	}
	inv.mu.Lock()
	defer inv.mu.Unlock()
	if _, ok := inv.skus[name]; ok {
		return ErrSKUExists
	}
	inv.skus[name] = &sku{name: name, total: total}
	return nil
}

// Available is Total - Committed - Reserved for the SKU right now.
func (inv *Inventory) Available(name string) (int, error) {
	st, err := inv.Stock(name)
	return st.Available, err
}

// Stock returns the SKU's counters.
func (inv *Inventory) Stock(name string) (Stock, error) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	inv.tick()
	s, ok := inv.skus[name]
	if !ok {
		return Stock{}, ErrUnknownSKU
	}
	return Stock{Total: s.total, Committed: s.committed, Reserved: s.reserved, Available: s.avail()}, nil
}

// normalize validates lines, sums duplicate SKUs and sorts by SKU.
func normalize(lines []Line) ([]Line, error) {
	if len(lines) == 0 {
		return nil, ErrNoLines
	}
	sum := map[string]int{}
	for _, l := range lines {
		if l.Qty <= 0 {
			return nil, ErrInvalidQty
		}
		sum[l.SKU] += l.Qty
	}
	out := make([]Line, 0, len(sum))
	for k, q := range sum {
		out = append(out, Line{SKU: k, Qty: q})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SKU < out[j].SKU })
	return out, nil
}

// reserveLocked places a hold on all lines or on none. Caller holds inv.mu and
// has called tick. Waitlist holds pass bypass=true; everyone else is refused
// while a SKU has waiters (no barging).
func (inv *Inventory) reserveLocked(now time.Time, lines []Line, ttl time.Duration) (*resv, error) {
	norm, err := normalize(lines)
	if err != nil {
		return nil, err
	}
	if ttl <= 0 {
		return nil, ErrInvalidTTL
	}
	for _, l := range norm {
		s, ok := inv.skus[l.SKU]
		if !ok {
			return nil, ErrUnknownSKU
		}
		if len(s.waiters) > 0 || s.avail() < l.Qty {
			return nil, ErrInsufficient
		}
	}
	return inv.newResv(now, norm, ttl, ""), nil
}

// newResv records a hold. Lines must be normalized and already checked.
func (inv *Inventory) newResv(now time.Time, norm []Line, ttl time.Duration, member string) *resv {
	r := &resv{id: inv.nextID("r"), lines: norm, expires: now.Add(ttl), idx: -1, member: member}
	for _, l := range norm {
		inv.skus[l.SKU].reserved += l.Qty
	}
	inv.resv[r.id] = r
	heap.Push(&inv.exp, r)
	return r
}

// Reserve holds qty units of sku for ttl. Errors: ErrUnknownSKU,
// ErrInsufficient (changes nothing), ErrInvalidQty, ErrInvalidTTL.
func (inv *Inventory) Reserve(name string, qty int, ttl time.Duration) (Reservation, error) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	now := inv.tick()
	r, err := inv.reserveLocked(now, []Line{{SKU: name, Qty: qty}}, ttl)
	if err != nil {
		return Reservation{}, err
	}
	return r.snapshot(), nil
}

// activeResv looks up id and requires it to be active.
func (inv *Inventory) activeResv(id string) (*resv, error) {
	r, ok := inv.resv[id]
	switch {
	case !ok:
		return nil, ErrNotFound
	case r.state == stExpired:
		return nil, ErrExpired
	case r.state != stActive:
		return nil, ErrAlreadyDone
	}
	return r, nil
}

// Commit turns an active reservation into a permanent removal of stock.
// Errors: ErrNotFound, ErrExpired, ErrAlreadyDone.
func (inv *Inventory) Commit(id string) error {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	now := inv.tick()
	r, err := inv.activeResv(id)
	if err != nil {
		return err
	}
	inv.commitLocked(now, r)
	return nil
}

func (inv *Inventory) commitLocked(now time.Time, r *resv) {
	heap.Remove(&inv.exp, r.idx)
	r.state, r.ended = stCommitted, now
	for _, l := range r.lines {
		s := inv.skus[l.SKU]
		s.reserved -= l.Qty
		s.committed += l.Qty
	}
}

// Release gives an active reservation's units back. Errors: ErrNotFound,
// ErrExpired (it already lapsed), ErrAlreadyDone.
func (inv *Inventory) Release(id string) error {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	now := inv.tick()
	r, err := inv.activeResv(id)
	if err != nil {
		return err
	}
	inv.releaseLocked(now, r)
	return nil
}

func (inv *Inventory) releaseLocked(now time.Time, r *resv) {
	heap.Remove(&inv.exp, r.idx)
	r.state, r.ended = stReleased, now
	for _, l := range r.lines {
		s := inv.skus[l.SKU]
		s.reserved -= l.Qty
		inv.serve(s, now)
	}
}
