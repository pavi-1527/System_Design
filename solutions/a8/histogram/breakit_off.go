//go:build !breakit

package histogram

// breakNoLock removes the Sketch's mutex when the package is built with
// -tags breakit (see breakit_on.go). Default: the lock is used.
const breakNoLock = false
