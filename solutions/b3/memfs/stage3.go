package memfs

import "errors"

// ErrNoSpace is returned by writes that would push total stored bytes over the
// quota. A failed write leaves the file exactly as it was.
var ErrNoSpace = errors.New("memfs: no space left (quota exceeded)")

// Usage is the size of a subtree, like `du`.
type Usage struct {
	Files int
	Dirs  int // includes the directory asked about
	Bytes int64
}

// SetQuota caps the total bytes stored in all files at maxBytes. Zero (or
// negative) removes the cap. It returns ErrNoSpace, changing nothing, if the
// file system already holds more than maxBytes.
//
// The quota is enforced atomically with each write, rename-overwrite and
// removal: under any number of concurrent writers the total never exceeds it.
func (fs *FS) SetQuota(maxBytes int64) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if maxBytes <= 0 {
		fs.max = 0
		return nil
	}
	if fs.used > maxBytes {
		return ErrNoSpace
	}
	fs.max = maxBytes
	return nil
}

// Usage returns the number of files, directories and bytes under p (p itself
// included). For a file: 1 file and its size.
func (fs *FS) Usage(p string) (Usage, error) {
	parts, err := split(p)
	if err != nil {
		return Usage{}, pathErr("usage", p, err)
	}
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	n, err := fs.walk(parts)
	if err != nil {
		return Usage{}, pathErr("usage", p, err)
	}
	var u Usage
	var rec func(*node)
	rec = func(n *node) {
		if !n.dir {
			u.Files++
			u.Bytes += n.size()
			return
		}
		u.Dirs++
		for _, c := range n.children {
			rec(c)
		}
	}
	rec(n)
	return u, nil
}
