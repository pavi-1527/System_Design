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

import (
	"fmt"
	"sort"
)

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
	out := make(Vector, len(v)+1)
	for k, n := range v {
		out[k] = n
	}
	out[device]++
	return out
}

// Compare orders two vectors.
func Compare(a, b Vector) Ordering {
	aLess, bLess := false, false
	for k, n := range a {
		switch m := b[k]; {
		case n < m:
			aLess = true
		case n > m:
			bLess = true
		}
	}
	for k, m := range b {
		if _, ok := a[k]; !ok && m > 0 {
			aLess = true
		}
	}
	switch {
	case aLess && bLess:
		return Concurrent
	case aLess:
		return Before
	case bLess:
		return After
	}
	return Equal
}

// Merge returns the entry-wise maximum.
func Merge(a, b Vector) Vector {
	out := make(Vector, len(a)+len(b))
	for k, n := range a {
		out[k] = n
	}
	for k, m := range b {
		if m > out[k] {
			out[k] = m
		}
	}
	return out
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
	d := Decision{Local: local, Remote: remote}
	switch Compare(local.Vector, remote.Vector) {
	case Equal, After:
		d.Action = KeepLocal
	case Before:
		d.Action = TakeRemote
	default:
		d.Action = Conflict
		d.ConflictCopy = fmt.Sprintf("%s (conflicted copy from %s)", local.Name, blame(local.Vector, remote.Vector))
	}
	return d
}

// blame names the remote device responsible for the divergence.
func blame(local, remote Vector) string {
	ids := make([]string, 0, len(remote))
	for id := range remote {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	best, bestN := "", uint64(0)
	for _, id := range ids {
		if n := remote[id]; n > local[id] && n > bestN {
			best, bestN = id, n
		}
	}
	return best
}
