// Package memfs is an in-memory file system: a tree of directories and files
// addressed by absolute, slash-separated paths.
//
// Lab B3 Unit 5 §1 (problem 10). Stage 1 is this file; Stage 2 lives in
// stage2.go and Stage 3 in stage3.go. Open those only when the interviewer
// (the unit text) has asked for them.
//
// Reference design: one tree of *node guarded by one sync.RWMutex. Every
// public operation parses and cleans its path first (no lock needed), then
// takes the lock once, so each operation is atomic and linearizable.
package memfs

import (
	"errors"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// Sentinel errors. Operations wrap them in a *PathError, so compare with
// errors.Is, never ==.
var (
	ErrInvalidPath = errors.New("memfs: invalid path")
	ErrNotExist    = errors.New("memfs: no such file or directory")
	ErrExist       = errors.New("memfs: file exists")
	ErrNotDir      = errors.New("memfs: not a directory")
	ErrIsDir       = errors.New("memfs: is a directory")
)

// PathError records which operation failed on which path.
type PathError struct {
	Op   string
	Path string
	Err  error
}

func (e *PathError) Error() string { return e.Op + " " + e.Path + ": " + e.Err.Error() }
func (e *PathError) Unwrap() error { return e.Err }

func pathErr(op, p string, err error) error { return &PathError{Op: op, Path: p, Err: err} }

// node is one directory entry. A directory has children and no data; a file
// has data and no children.
type node struct {
	name     string
	dir      bool
	data     []byte
	children map[string]*node
	mod      time.Time
}

func (n *node) size() int64 { return int64(len(n.data)) }

// FS is an in-memory file system. The zero value is not usable; call New.
// All methods are safe for concurrent use.
type FS struct {
	mu   sync.RWMutex
	root *node
	now  func() time.Time
	max  int64 // quota in bytes; 0 = unlimited (Stage 3)
	used int64 // bytes currently stored in files
}

// New returns an empty file system containing only the root directory "/".
func New() *FS { return NewWithClock(time.Now) }

// split validates p and returns its cleaned components; the root has none.
// Paths must be absolute. "." and ".." are resolved lexically, which is only
// correct because this file system has no symbolic links.
func split(p string) ([]string, error) {
	if p == "" || p[0] != '/' || strings.IndexByte(p, 0) >= 0 {
		return nil, ErrInvalidPath
	}
	c := path.Clean(p)
	if c == "/" {
		return nil, nil
	}
	return strings.Split(c[1:], "/"), nil
}

// walk resolves parts from the root. A missing component is ErrNotExist; a
// file in the middle of the path is ErrNotDir. Caller holds fs.mu.
func (fs *FS) walk(parts []string) (*node, error) {
	n := fs.root
	for _, name := range parts {
		if !n.dir {
			return nil, ErrNotDir
		}
		c, ok := n.children[name]
		if !ok {
			return nil, ErrNotExist
		}
		n = c
	}
	return n, nil
}

// parentOf resolves the directory that holds the last component of parts.
func (fs *FS) parentOf(parts []string) (*node, string, error) {
	p, err := fs.walk(parts[:len(parts)-1])
	if err != nil {
		return nil, "", err
	}
	if !p.dir {
		return nil, "", ErrNotDir
	}
	return p, parts[len(parts)-1], nil
}

// Mkdir creates one directory. Its parent must already exist.
// Errors: ErrExist (including "/"), ErrNotExist (missing parent), ErrNotDir
// (a parent component is a file), ErrInvalidPath.
func (fs *FS) Mkdir(p string) error {
	parts, err := split(p)
	if err != nil {
		return pathErr("mkdir", p, err)
	}
	if len(parts) == 0 {
		return pathErr("mkdir", p, ErrExist)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	parent, name, err := fs.parentOf(parts)
	if err != nil {
		return pathErr("mkdir", p, err)
	}
	if _, ok := parent.children[name]; ok {
		return pathErr("mkdir", p, ErrExist)
	}
	t := fs.now()
	parent.children[name] = &node{name: name, dir: true, children: map[string]*node{}, mod: t}
	parent.mod = t
	return nil
}

// MkdirAll creates p and any missing parents, like `mkdir -p`. It returns nil
// if p is already a directory and ErrNotDir if any component is a file. On
// error nothing has been created.
func (fs *FS) MkdirAll(p string) error {
	parts, err := split(p)
	if err != nil {
		return pathErr("mkdirall", p, err)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	n := fs.root
	for _, name := range parts {
		c, ok := n.children[name]
		if !ok {
			t := fs.now()
			c = &node{name: name, dir: true, children: map[string]*node{}, mod: t}
			n.children[name] = c
			n.mod = t
		} else if !c.dir {
			return pathErr("mkdirall", p, ErrNotDir)
		}
		n = c
	}
	return nil
}

// WriteFile creates or truncates the file at p with a copy of data. The
// parent directory must exist. Errors: ErrIsDir if p is a directory (or "/"),
// ErrNotExist, ErrNotDir, ErrNoSpace (Stage 3).
func (fs *FS) WriteFile(p string, data []byte) error {
	parts, err := split(p)
	if err != nil {
		return pathErr("write", p, err)
	}
	if len(parts) == 0 {
		return pathErr("write", p, ErrIsDir)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	parent, name, err := fs.parentOf(parts)
	if err != nil {
		return pathErr("write", p, err)
	}
	old, exists := parent.children[name]
	if exists && old.dir {
		return pathErr("write", p, ErrIsDir)
	}
	var oldSize int64
	if exists {
		oldSize = old.size()
	}
	delta := int64(len(data)) - oldSize
	if fs.max > 0 && fs.used+delta > fs.max {
		return pathErr("write", p, ErrNoSpace)
	}
	t := fs.now()
	buf := append([]byte(nil), data...)
	if exists {
		old.data, old.mod = buf, t
	} else {
		parent.children[name] = &node{name: name, data: buf, mod: t}
		parent.mod = t
	}
	fs.used += delta
	return nil
}

// ReadFile returns a copy of the file's contents. Errors: ErrNotExist,
// ErrIsDir, ErrNotDir.
func (fs *FS) ReadFile(p string) ([]byte, error) {
	parts, err := split(p)
	if err != nil {
		return nil, pathErr("read", p, err)
	}
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	n, err := fs.walk(parts)
	if err != nil {
		return nil, pathErr("read", p, err)
	}
	if n.dir {
		return nil, pathErr("read", p, ErrIsDir)
	}
	return append([]byte(nil), n.data...), nil
}

// List returns the names (not full paths) in directory p, sorted. For a file
// it returns just the file's own name, like `ls file`.
func (fs *FS) List(p string) ([]string, error) {
	parts, err := split(p)
	if err != nil {
		return nil, pathErr("list", p, err)
	}
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	n, err := fs.walk(parts)
	if err != nil {
		return nil, pathErr("list", p, err)
	}
	if !n.dir {
		return []string{n.name}, nil
	}
	names := make([]string, 0, len(n.children))
	for name := range n.children {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
