// Package clocks implements logical time: Lamport clocks, vector clocks, and
// two single-key registers that show what each buys you under clock skew.
//
// Lab A5 Unit 2 §1–§3. Why this matters: a replicated store that resolves
// concurrent writes with wall-clock timestamps ("last write wins") silently
// loses data whenever two machines disagree about the time. Logical clocks
// track causality (who had seen what) instead of the time of day, so a store
// can tell "this write replaces that one" from "these two writes are
// concurrent; keep both".
//
// Suggested order: Lamport and Stamp (the warm-up), then Vector.Tick, Compare,
// Merge and Descends, then Reconcile, then the two registers. Run:
//
//	go test -race ./labs/a5/clocks/
//
// Conventions used throughout:
//   - A Vector maps an actor (node or client ID) to a counter. A missing entry
//     means 0, so Vector{"a": 0} and Vector{} are the same clock. Functions
//     that return a Vector never include zero entries and never alias their
//     inputs: callers may mutate a result without affecting anything else.
//   - A nil Vector is a valid empty clock everywhere.
package clocks

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// Lamport clocks
// ---------------------------------------------------------------------------

// Lamport is a Lamport logical clock (Lamport, 1978). The zero value is a
// clock at time 0 and is ready to use. It is safe for concurrent use.
//
// Rules: before a local event or a send, increment (Tick). On receiving a
// message stamped with the sender's time, set the clock to max(local, remote)
// and then increment (Observe). If event a happened-before event b, then
// time(a) < time(b). The converse does NOT hold.
type Lamport struct {
	// TODO(lab): a mutex and a counter. Every method must hold the lock:
	// Tick is called from many goroutines in the tests.
	mu sync.Mutex
}

// Time returns the current value without changing it.
func (l *Lamport) Time() uint64 {
	// TODO(lab)
	return 0
}

// Tick records a local event (or a send) and returns the new time.
func (l *Lamport) Tick() uint64 {
	// TODO(lab)
	return 0
}

// Observe records the receipt of a message stamped remote and returns the new
// time: max(local, remote) + 1.
func (l *Lamport) Observe(remote uint64) uint64 {
	// TODO(lab)
	_ = remote
	return 0
}

// Stamp is a Lamport time plus the node that produced it. Comparing stamps by
// (Time, Node) gives a total order that is consistent with causality, which
// is what you need for things like "who gets the lock first".
type Stamp struct {
	Time uint64
	Node string
}

// Less reports whether a sorts before b: smaller Time first, and for equal
// Times the lexicographically smaller Node first.
func (a Stamp) Less(b Stamp) bool {
	// TODO(lab)
	_ = b
	return false
}

// ---------------------------------------------------------------------------
// Vector clocks
// ---------------------------------------------------------------------------

// Ordering is the causal relationship between two clocks. The zero value is
// deliberately not a valid Ordering, so a stub that returns 0 fails loudly.
type Ordering int

// The possible relationships between clocks a and b, as returned by
// Compare(a, b).
const (
	Equal      Ordering = iota + 1 // same clock
	Before                         // a happened before b (b has seen everything a has, and more)
	After                          // b happened before a
	Concurrent                     // neither has seen the other
)

func (o Ordering) String() string {
	switch o {
	case Equal:
		return "equal"
	case Before:
		return "before"
	case After:
		return "after"
	case Concurrent:
		return "concurrent"
	}
	return fmt.Sprintf("Ordering(%d)", int(o))
}

// Vector is a vector clock: one counter per actor.
type Vector map[string]uint64

// Clone returns an independent copy without zero entries. (Provided.)
func (v Vector) Clone() Vector {
	out := make(Vector, len(v))
	for k, n := range v {
		if n != 0 {
			out[k] = n
		}
	}
	return out
}

// String renders the clock with sorted keys, e.g. "{a:2 b:1}". (Provided.)
func (v Vector) String() string {
	keys := make([]string, 0, len(v))
	for k, n := range v {
		if n != 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s:%d", k, v[k])
	}
	return "{" + strings.Join(parts, " ") + "}"
}

// Tick returns a copy of v with actor's counter incremented by one. v itself
// is not modified.
func (v Vector) Tick(actor string) Vector {
	// TODO(lab)
	_ = actor
	return nil
}

// Descends reports whether v has seen everything o has: v[k] >= o[k] for
// every actor k. Equal clocks descend each other.
func (v Vector) Descends(o Vector) bool {
	// TODO(lab)
	_ = o
	return false
}

// Compare returns the causal relationship of a to b:
//   - Equal if both descend each other,
//   - After if a strictly descends b (b happened before a),
//   - Before if b strictly descends a,
//   - Concurrent if neither descends the other.
func Compare(a, b Vector) Ordering {
	// TODO(lab): two calls to Descends are enough.
	_, _ = a, b
	return 0
}

// Merge returns the pointwise maximum of a and b: the smallest clock that
// descends both. It is commutative, associative and idempotent.
func Merge(a, b Vector) Vector {
	// TODO(lab)
	_, _ = a, b
	return nil
}

// ---------------------------------------------------------------------------
// Versioned values and reconciliation
// ---------------------------------------------------------------------------

// Versioned is a value tagged with the vector clock of the write that
// produced it.
type Versioned struct {
	Clock Vector
	Value string
}

// Reconcile takes any collection of versions (for instance everything read
// from R replicas) and returns the "maximal" ones: a version is dropped if
// another version in the input strictly descends it, and exact duplicates
// (equal clock AND equal value) are collapsed to one. What remains are
// siblings: writes that are concurrent with each other.
//
// The result keeps the first-seen order of the surviving versions. The input
// slice and its clocks are not modified. Reconcile(nil) returns nil.
//
// Two versions with equal clocks but different values cannot happen if every
// actor increments only its own entry and never reuses a context; Reconcile
// keeps both as siblings if it ever sees that.
func Reconcile(vs []Versioned) []Versioned {
	// TODO(lab): O(n^2) is fine. Clone the clocks you return.
	_ = vs
	return nil
}

// ---------------------------------------------------------------------------
// Registers: last-write-wins vs siblings
// ---------------------------------------------------------------------------

// LWWRegister is a single-value register that resolves conflicts by physical
// timestamp: the highest timestamp wins, everything else is discarded. This is
// what "last write wins" means in Cassandra, DynamoDB global tables, and most
// systems that offer it. The zero value is an empty register. Not safe for
// concurrent use.
type LWWRegister struct {
	// TODO(lab): value string, ts int64, set bool.
	set bool
}

// Write offers (value, ts) to the register and reports whether it was
// accepted. It is accepted if the register is empty, or ts is greater than the
// stored timestamp, or ts is equal and value is lexicographically greater (a
// deterministic tie-break so replicas converge). Otherwise it is silently
// dropped.
func (r *LWWRegister) Write(value string, ts int64) bool {
	// TODO(lab)
	_, _ = value, ts
	return false
}

// Merge folds another replica's state into r (anti-entropy). It is
// equivalent to Write(o.value, o.ts) when o is non-empty and a no-op otherwise.
func (r *LWWRegister) Merge(o *LWWRegister) {
	// TODO(lab)
	_ = o
}

// Get returns the stored value, its timestamp, and whether anything was ever
// written.
func (r *LWWRegister) Get() (value string, ts int64, ok bool) {
	// TODO(lab)
	return "", 0, false
}

// SiblingRegister keeps every version that has not been superseded, using
// vector clocks to decide. A client reads (values + context), decides, and
// writes back passing the context, which supersedes exactly what it saw.
// The zero value is an empty register. Not safe for concurrent use.
type SiblingRegister struct {
	// TODO(lab): versions []Versioned.
	versions []Versioned
}

// Put stores value on behalf of actor, written against context ctx (the
// context returned by a previous Get, or nil for a blind write). The new
// version's clock is ctx.Tick(actor). The new version replaces every stored
// version whose clock it descends; versions concurrent with it are kept as
// siblings. If some stored version strictly descends the new clock, the write
// is stale and is dropped. Put returns the new clock either way.
func (r *SiblingRegister) Put(actor string, ctx Vector, value string) Vector {
	// TODO(lab): build the new version, then Reconcile(old + new).
	_, _, _ = actor, ctx, value
	return nil
}

// Merge folds another replica's versions into r: union, then Reconcile.
func (r *SiblingRegister) Merge(o *SiblingRegister) {
	// TODO(lab)
	_ = o
}

// Get returns the surviving versions sorted by Value (then by clock string)
// and a context to pass to the next Put: the Merge of all their clocks. An
// empty register returns (nil, nil). The returned slices and clocks must be
// copies.
func (r *SiblingRegister) Get() (versions []Versioned, ctx Vector) {
	// TODO(lab)
	return nil, nil
}
