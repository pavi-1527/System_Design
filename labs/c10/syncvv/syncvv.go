// Package syncvv decides what a sync client does when it sees a file version
// from the server: version vectors tell it whether the remote version
// descends from its own (take it), its own descends from the remote (push
// it), or the two were edited concurrently (a conflict, which file-sync
// products resolve by keeping both as a "conflicted copy").
//
// It is the STRETCH lab for C10 Unit 2 §7 (not gated). Run:
// go test -race ./labs/c10/syncvv/
//
// Contract (the tests depend on every line):
//   - A Vector maps a device id to the number of edits that device has made.
//     Missing entries count as zero. Vectors are values: Increment and Merge
//     return new maps and never modify their inputs.
//   - Compare(a, b) is Equal when every entry matches, Before when a <= b
//     entry-wise and some entry is strictly smaller, After when the reverse
//     holds, and Concurrent when each has an entry greater than the other's.
//   - Merge(a, b) is the entry-wise maximum.
//   - Resolve(local, remote) returns KeepLocal when the versions are equal or
//     the remote is Before the local one, TakeRemote when the local is
//     Before the remote one, and Conflict when they are Concurrent. A
//     Conflict carries both versions and the name of a conflicted copy:
//     "<name> (conflicted copy from <remote device>)" where the remote
//     device is the id with the largest count in the remote vector that is
//     greater than the local vector's count for it (ties broken by the
//     smallest id).
package syncvv

// Vector is a version vector keyed by device id.
type Vector map[string]uint64

// Ordering is the result of Compare.
type Ordering int

// Orderings.
const (
	Equal Ordering = iota
	Before
	After
	Concurrent
)

// String is GIVEN.
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
	return "invalid"
}

// Increment returns a copy of v with device's count increased by one.
func (v Vector) Increment(device string) Vector {
	// TODO(lab)
	_ = device
	return nil
}

// Compare orders two vectors.
func Compare(a, b Vector) Ordering {
	// TODO(lab): walk both maps; track "a has something smaller" and
	// "b has something smaller".
	_, _ = a, b
	return Concurrent
}

// Merge returns the entry-wise maximum.
func Merge(a, b Vector) Vector {
	// TODO(lab)
	_, _ = a, b
	return nil
}

// Version is one version of a file as a client sees it.
type Version struct {
	Name    string
	Vector  Vector
	Content []byte
}

// Action is what the client should do.
type Action int

// Actions.
const (
	KeepLocal Action = iota + 1
	TakeRemote
	Conflict
)

// String is GIVEN.
func (a Action) String() string {
	switch a {
	case KeepLocal:
		return "keep-local"
	case TakeRemote:
		return "take-remote"
	case Conflict:
		return "conflict"
	}
	return "invalid"
}

// Decision is the outcome of Resolve.
type Decision struct {
	Action        Action
	ConflictCopy  string // name for the remote version when Action == Conflict
	Local, Remote Version
}

// Resolve decides between a local and a remote version of the same file.
func Resolve(local, remote Version) Decision {
	// TODO(lab)
	return Decision{Local: local, Remote: remote}
}
