package quorum

// This file is PROVIDED (identical in the lab and the solution). It is the
// storage side of the lab: versioned values, a per-replica store, and hints.
// Your job is the coordinator in quorum.go.
//
// Versioning, in one paragraph. Every Put carries the clock the client got
// from its last Get (its "context"), and the store ticks the client's own
// entry to make the new version's clock. Version X supersedes version Y when
// X's clock has seen everything Y's has. If neither has seen the other, they
// are concurrent and BOTH are kept as siblings; the application merges them
// later. Unit 2 builds this machinery from scratch in labs/a5/clocks; here it
// is given so you can focus on quorums.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// VClock is a vector clock keyed by client ID. A missing entry means 0.
type VClock map[string]uint64

// Clone returns an independent copy without zero entries.
func (c VClock) Clone() VClock {
	out := make(VClock, len(c))
	for k, n := range c {
		if n != 0 {
			out[k] = n
		}
	}
	return out
}

// Tick returns a copy of c with actor's counter incremented.
func (c VClock) Tick(actor string) VClock {
	out := c.Clone()
	out[actor]++
	return out
}

// Descends reports whether c has seen everything o has.
func (c VClock) Descends(o VClock) bool {
	for k, n := range o {
		if c[k] < n {
			return false
		}
	}
	return true
}

// Merge returns the pointwise maximum of c and o.
func (c VClock) Merge(o VClock) VClock {
	out := c.Clone()
	for k, n := range o {
		if n > out[k] {
			out[k] = n
		}
	}
	return out
}

// String renders the clock with sorted keys, e.g. "{alice:2 bob:1}".
func (c VClock) String() string {
	keys := make([]string, 0, len(c))
	for k, n := range c {
		if n != 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s:%d", k, c[k])
	}
	return "{" + strings.Join(parts, " ") + "}"
}

// Version is one value of a key together with the clock of the write that
// produced it.
type Version struct {
	Clock VClock
	Value string
}

func (v Version) clone() Version { return Version{Clock: v.Clock.Clone(), Value: v.Value} }

// Reconcile returns the versions that are not superseded by another version in
// vs: a version is dropped if another version's clock strictly descends it.
// Exact duplicates (same clock and same value) collapse to one. The result is
// sorted by (clock string, value) so that equal sets compare equal.
func Reconcile(vs []Version) []Version {
	var out []Version
	for i, v := range vs {
		dominated := false
		for j, w := range vs {
			if i == j {
				continue
			}
			vw, wv := v.Clock.Descends(w.Clock), w.Clock.Descends(v.Clock)
			switch {
			case wv && !vw:
				dominated = true
			case wv && vw && v.Value == w.Value && j < i:
				dominated = true
			}
			if dominated {
				break
			}
		}
		if !dominated {
			out = append(out, v.clone())
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Clock.String(), out[j].Clock.String()
		if a != b {
			return a < b
		}
		return out[i].Value < out[j].Value
	})
	return out
}

func sameVersions(a, b []Version) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Value != b[i].Value || a[i].Clock.String() != b[i].Clock.String() {
			return false
		}
	}
	return true
}

// Hint is a write held by a stand-in node on behalf of Target, which was
// unreachable when the write happened (hinted handoff).
type Hint struct {
	Target  string
	Key     string
	Version Version
}

// Node is one replica's local storage. Safe for concurrent use.
type Node struct {
	id    string
	mu    sync.Mutex
	data  map[string][]Version
	hints []Hint
}

// NewNode returns an empty replica.
func NewNode(id string) *Node { return &Node{id: id, data: map[string][]Version{}} }

// ID returns the node's identifier.
func (n *Node) ID() string { return n.id }

// Apply merges v into the replica's versions of key (idempotent, commutative:
// applying the same set of versions in any order gives the same result).
func (n *Node) Apply(key string, v Version) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.data[key] = Reconcile(append(append([]Version(nil), n.data[key]...), v))
}

// Versions returns a copy of the replica's current versions of key (nil if none).
func (n *Node) Versions(key string) []Version {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]Version, len(n.data[key]))
	for i, v := range n.data[key] {
		out[i] = v.clone()
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// AddHint stores a hint on this node. Hints are kept apart from the node's own
// data: a stand-in does not serve them to readers.
func (n *Node) AddHint(h Hint) {
	n.mu.Lock()
	defer n.mu.Unlock()
	h.Version = h.Version.clone()
	n.hints = append(n.hints, h)
}

// Hints returns a copy of the hints currently held.
func (n *Node) Hints() []Hint {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]Hint(nil), n.hints...)
}

// TakeHints removes and returns every hint whose Target satisfies ready.
func (n *Node) TakeHints(ready func(target string) bool) []Hint {
	n.mu.Lock()
	defer n.mu.Unlock()
	var taken, keep []Hint
	for _, h := range n.hints {
		if ready(h.Target) {
			taken = append(taken, h)
		} else {
			keep = append(keep, h)
		}
	}
	n.hints = keep
	return taken
}
