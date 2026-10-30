package memfs

import (
	"errors"
	"time"
)

// Stage 2 (≈ 25 min): metadata, deletion, moving, and traversal.

// Stage 2 errors.
var (
	ErrNotEmpty    = errors.New("memfs: directory not empty")
	ErrInvalidMove = errors.New("memfs: cannot move a directory into itself")
)

// ErrSkipDir may be returned by a Walk callback invoked on a directory to skip
// that directory's contents. Walk itself then continues and returns nil.
var ErrSkipDir = errors.New("memfs: skip this directory")

// Info describes a file or directory.
type Info struct {
	Name    string // base name; "/" for the root
	IsDir   bool
	Size    int64 // bytes for files, 0 for directories
	ModTime time.Time
}

// NewWithClock returns an empty file system that reads time from now, so tests
// can control ModTime. A file's ModTime is its last write; a directory's is the
// last time its set of entries changed (an entry created, removed, or renamed
// in or out). Overwriting an existing file changes the file's ModTime but not
// its directory's. Renaming a node does not change the node's own ModTime.
func NewWithClock(now func() time.Time) *FS {
	// TODO(lab): New() should become NewWithClock(time.Now).
	return &FS{now: now}
}

// Stat returns metadata for p.
func (fs *FS) Stat(p string) (Info, error) {
	// TODO(lab)
	_ = p
	return Info{}, errNotImplemented
}

// Remove deletes a file or an EMPTY directory.
// Errors: ErrNotExist, ErrNotEmpty, ErrInvalidPath (also for "/").
func (fs *FS) Remove(p string) error {
	// TODO(lab)
	_ = p
	return errNotImplemented
}

// RemoveAll deletes p and everything under it. It returns nil if p does not
// exist. Removing "/" is ErrInvalidPath.
func (fs *FS) RemoveAll(p string) error {
	// TODO(lab)
	_ = p
	return errNotImplemented
}

// Rename moves the file or directory at oldp (with its whole subtree) to newp.
// It is atomic: no other operation ever sees it half done.
//
//   - oldp == newp (after cleaning) is a no-op returning nil.
//   - newp's parent must exist (ErrNotExist) and be a directory (ErrNotDir).
//   - If newp exists: a file replaces a file; a directory replaces an EMPTY
//     directory; file onto directory is ErrIsDir, directory onto file is
//     ErrNotDir, directory onto a non-empty directory is ErrNotEmpty.
//   - Moving a directory into its own subtree is ErrInvalidMove. Beware:
//     "/ab" is not inside "/a".
//   - "/" can be neither source nor destination (ErrInvalidPath).
//   - A failed Rename changes nothing.
func (fs *FS) Rename(oldp, newp string) error {
	// TODO(lab)
	_, _ = oldp, newp
	return errNotImplemented
}

// Walk calls fn for root and every descendant, parents before children,
// siblings in name order. The path passed to fn is the cleaned absolute path.
// If fn returns ErrSkipDir on a directory its contents are skipped; any other
// non-nil error stops the walk and is returned. Walking a file calls fn once.
//
// fn may call other FS methods, including writes: do not hold your lock while
// calling it. Walk must nevertheless show a consistent view: it must never
// show a node twice or not at all because of a concurrent Rename (Stage 3
// tests this). Think about what that implies.
func (fs *FS) Walk(root string, fn func(path string, info Info) error) error {
	// TODO(lab)
	_, _ = root, fn
	return errNotImplemented
}
