package memfs

import "errors"

// Stage 3 (≈ 20 min): capacity limits and the concurrency contract.

// ErrNoSpace is returned by writes that would push the total bytes stored in
// files over the quota. A rejected write must leave the file system exactly as
// it was (an overwrite must not truncate the old content first).
var ErrNoSpace = errors.New("memfs: no space left (quota exceeded)")

// Usage is the size of a subtree, like `du`.
type Usage struct {
	Files int
	Dirs  int // includes the directory asked about
	Bytes int64
}

// SetQuota caps the total bytes stored in all files at maxBytes. Zero or
// negative means unlimited. It returns ErrNoSpace, changing nothing, if the
// file system already holds more than maxBytes.
//
// The cap must hold under any number of concurrent writers: check-then-write
// must be one atomic step. Overwrites count only their growth; shrinking
// writes, Remove, RemoveAll and a Rename that replaces a file all free space.
func (fs *FS) SetQuota(maxBytes int64) error {
	// TODO(lab)
	_ = maxBytes
	return errNotImplemented
}

// Usage returns the number of files, directories and bytes under p (p itself
// included; for a file: 1 file and its size).
func (fs *FS) Usage(p string) (Usage, error) {
	// TODO(lab)
	_ = p
	return Usage{}, errNotImplemented
}
