package memfs

import (
	"errors"
	"sort"
	"time"
)

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
// last time its set of entries changed (create, remove, rename in or out).
func NewWithClock(now func() time.Time) *FS {
	t := now()
	return &FS{now: now, root: &node{name: "/", dir: true, children: map[string]*node{}, mod: t}}
}

func (n *node) info() Info {
	i := Info{Name: n.name, IsDir: n.dir, ModTime: n.mod}
	if !n.dir {
		i.Size = n.size()
	}
	return i
}

// Stat returns metadata for p.
func (fs *FS) Stat(p string) (Info, error) {
	parts, err := split(p)
	if err != nil {
		return Info{}, pathErr("stat", p, err)
	}
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	n, err := fs.walk(parts)
	if err != nil {
		return Info{}, pathErr("stat", p, err)
	}
	return n.info(), nil
}

// treeBytes is the number of file bytes in the subtree rooted at n.
func treeBytes(n *node) int64 {
	if !n.dir {
		return n.size()
	}
	var sum int64
	for _, c := range n.children {
		sum += treeBytes(c)
	}
	return sum
}

// Remove deletes a file or an EMPTY directory. Errors: ErrNotExist,
// ErrNotEmpty, ErrInvalidPath for "/".
func (fs *FS) Remove(p string) error {
	parts, err := split(p)
	if err != nil {
		return pathErr("remove", p, err)
	}
	if len(parts) == 0 {
		return pathErr("remove", p, ErrInvalidPath)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	parent, name, err := fs.parentOf(parts)
	if err != nil {
		return pathErr("remove", p, err)
	}
	n, ok := parent.children[name]
	if !ok {
		return pathErr("remove", p, ErrNotExist)
	}
	if n.dir && len(n.children) > 0 {
		return pathErr("remove", p, ErrNotEmpty)
	}
	delete(parent.children, name)
	parent.mod = fs.now()
	fs.used -= n.size()
	return nil
}

// RemoveAll deletes p and everything under it. It returns nil if p does not
// exist. Removing "/" is an error (ErrInvalidPath).
func (fs *FS) RemoveAll(p string) error {
	parts, err := split(p)
	if err != nil {
		return pathErr("removeall", p, err)
	}
	if len(parts) == 0 {
		return pathErr("removeall", p, ErrInvalidPath)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	parent, name, err := fs.parentOf(parts)
	if err != nil {
		return nil // nothing there (missing or file-in-the-way): nothing to remove
	}
	n, ok := parent.children[name]
	if !ok {
		return nil
	}
	delete(parent.children, name)
	parent.mod = fs.now()
	fs.used -= treeBytes(n)
	return nil
}

// isPrefix reports whether a is a proper prefix of b, compared component by
// component. ("/a" is not a prefix of "/ab"; string prefixes would say it is.)
func isPrefix(a, b []string) bool {
	if len(a) >= len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Rename moves the file or directory at oldp (with its whole subtree) to newp.
// It is atomic: no other operation sees it half done.
//
//   - oldp == newp (after cleaning) is a no-op.
//   - newp's parent must exist.
//   - If newp exists: a file replaces a file; a directory replaces an EMPTY
//     directory; file onto directory is ErrIsDir, directory onto file is
//     ErrNotDir, directory onto a non-empty directory is ErrNotEmpty.
//   - Moving a directory into its own subtree is ErrInvalidMove.
//   - "/" can be neither source nor destination (ErrInvalidPath).
func (fs *FS) Rename(oldp, newp string) error {
	op, err := split(oldp)
	if err != nil {
		return pathErr("rename", oldp, err)
	}
	np, err := split(newp)
	if err != nil {
		return pathErr("rename", newp, err)
	}
	if len(op) == 0 {
		return pathErr("rename", oldp, ErrInvalidPath)
	}
	if len(np) == 0 {
		return pathErr("rename", newp, ErrInvalidPath)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	srcParent, srcName, err := fs.parentOf(op)
	if err != nil {
		return pathErr("rename", oldp, err)
	}
	src, ok := srcParent.children[srcName]
	if !ok {
		return pathErr("rename", oldp, ErrNotExist)
	}
	if src.dir && isPrefix(op, np) {
		return pathErr("rename", newp, ErrInvalidMove)
	}
	dstParent, dstName, err := fs.parentOf(np)
	if err != nil {
		return pathErr("rename", newp, err)
	}
	if dstParent == srcParent && dstName == srcName {
		return nil
	}
	if dst, exists := dstParent.children[dstName]; exists {
		switch {
		case !src.dir && dst.dir:
			return pathErr("rename", newp, ErrIsDir)
		case src.dir && !dst.dir:
			return pathErr("rename", newp, ErrNotDir)
		case dst.dir && len(dst.children) > 0:
			return pathErr("rename", newp, ErrNotEmpty)
		}
		fs.used -= dst.size() // replaced file's bytes are freed
	}
	delete(srcParent.children, srcName)
	src.name = dstName
	dstParent.children[dstName] = src
	t := fs.now()
	srcParent.mod, dstParent.mod = t, t
	return nil
}

type entry struct {
	path string
	info Info
	dir  bool
}

// snapshot collects n and its descendants in pre-order, children sorted by
// name. Caller holds fs.mu.
func snapshot(p string, n *node, out []entry) []entry {
	out = append(out, entry{path: p, info: n.info(), dir: n.dir})
	if !n.dir {
		return out
	}
	names := make([]string, 0, len(n.children))
	for name := range n.children {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		child := p + "/" + name
		if p == "/" {
			child = "/" + name
		}
		out = snapshot(child, n.children[name], out)
	}
	return out
}

// Walk calls fn for root and every descendant, parents before children,
// siblings in name order. paths are cleaned absolute paths. If fn returns
// ErrSkipDir on a directory its contents are skipped; any other non-nil error
// stops the walk and is returned.
//
// Walk takes a consistent snapshot under the read lock and calls fn after
// releasing it, so fn may freely call other FS methods (including writes).
// The cost is O(subtree) memory, and fn can observe entries that were removed
// after the snapshot was taken.
func (fs *FS) Walk(root string, fn func(path string, info Info) error) error {
	parts, err := split(root)
	if err != nil {
		return pathErr("walk", root, err)
	}
	fs.mu.RLock()
	n, err := fs.walk(parts)
	if err != nil {
		fs.mu.RUnlock()
		return pathErr("walk", root, err)
	}
	start := "/"
	if len(parts) > 0 {
		start = "/" + joinParts(parts)
	}
	entries := snapshot(start, n, nil)
	fs.mu.RUnlock()

	skip := "" // path of a directory whose contents we are skipping
	for _, e := range entries {
		if skip != "" {
			if len(e.path) > len(skip) && e.path[:len(skip)] == skip && e.path[len(skip)] == '/' {
				continue
			}
			skip = ""
		}
		switch err := fn(e.path, e.info); {
		case err == nil:
		case errors.Is(err, ErrSkipDir) && e.dir:
			skip = e.path
			if skip == "/" {
				return nil
			}
		default:
			return err
		}
	}
	return nil
}

func joinParts(parts []string) string {
	s := parts[0]
	for _, p := range parts[1:] {
		s += "/" + p
	}
	return s
}
