// Package memfs is an in-memory file system: a tree of directories and files
// addressed by absolute, slash-separated paths.
//
// Lab B3 Unit 5 §1 (problem 10), time box 75 minutes. Work in stages and open
// the next stage's file only when you get there, like an interviewer who adds
// requirements as you go:
//
//	Stage 1 (≈ 30 min)  this file:  Mkdir, MkdirAll, WriteFile, ReadFile, List
//	Stage 2 (≈ 25 min)  stage2.go:  Stat, Remove, RemoveAll, Rename, Walk, a clock
//	Stage 3 (≈ 20 min)  stage3.go:  quota, Usage, concurrency guarantees
//
// Run one stage at a time:
//
//	go test -race -run 'Stage1' ./labs/b3/memfs/
//
// All methods must be safe for concurrent use, even though Stage 1's tests are
// sequential: decide your locking model now, because retrofitting it at minute
// 60 is how timed attempts fail.
package memfs

import (
	"errors"
	"time"
)

// Sentinel errors. Return them wrapped in a *PathError (op, path, cause) so
// callers can use errors.Is. Tests only ever use errors.Is.
var (
	ErrInvalidPath = errors.New("memfs: invalid path")
	ErrNotExist    = errors.New("memfs: no such file or directory")
	ErrExist       = errors.New("memfs: file exists")
	ErrNotDir      = errors.New("memfs: not a directory")
	ErrIsDir       = errors.New("memfs: is a directory")
)

var errNotImplemented = errors.New("memfs: not implemented")

// PathError records which operation failed on which path.
type PathError struct {
	Op   string
	Path string
	Err  error
}

func (e *PathError) Error() string { return e.Op + " " + e.Path + ": " + e.Err.Error() }
func (e *PathError) Unwrap() error { return e.Err }

// FS is an in-memory file system. All methods are safe for concurrent use.
type FS struct {
	// TODO(lab): the root node, a lock, and whatever else you need.
	now func() time.Time
}

// New returns an empty file system containing only the root directory "/".
func New() *FS {
	// TODO(lab)
	return &FS{now: time.Now}
}

// Path rules (all operations): paths must be absolute (start with "/");
// anything else, the empty string, or a path containing a NUL byte is
// ErrInvalidPath. Clean paths lexically first: "//a/./b/../c/" is "/a/c", and
// "/.." is "/". (Lexical cleaning is only correct because there are no
// symbolic links.)

// Mkdir creates one directory. Its parent must already exist.
// Errors: ErrExist (also for "/"), ErrNotExist (missing parent), ErrNotDir
// (a parent component is a file), ErrInvalidPath.
func (fs *FS) Mkdir(p string) error {
	// TODO(lab)
	_ = p
	return errNotImplemented
}

// MkdirAll creates p and any missing parents, like `mkdir -p`. It returns nil
// if p is already a directory and ErrNotDir if any component is a file. On
// error nothing may have been created.
func (fs *FS) MkdirAll(p string) error {
	// TODO(lab)
	_ = p
	return errNotImplemented
}

// WriteFile creates or truncates the file at p with a COPY of data (the
// caller may reuse its slice). The parent directory must exist.
// Errors: ErrIsDir if p is a directory (or "/"), ErrNotExist (missing
// parent), ErrNotDir, ErrInvalidPath.
func (fs *FS) WriteFile(p string, data []byte) error {
	// TODO(lab)
	_, _ = p, data
	return errNotImplemented
}

// ReadFile returns a COPY of the file's contents (the caller may modify it).
// Errors: ErrNotExist, ErrIsDir, ErrNotDir, ErrInvalidPath.
func (fs *FS) ReadFile(p string) ([]byte, error) {
	// TODO(lab)
	_ = p
	return nil, errNotImplemented
}

// List returns the names (not full paths) in directory p, sorted in byte
// order. For a file it returns just the file's own name, like `ls file`.
func (fs *FS) List(p string) ([]string, error) {
	// TODO(lab)
	_ = p
	return nil, errNotImplemented
}
