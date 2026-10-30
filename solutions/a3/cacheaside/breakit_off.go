//go:build !breakit

package cacheaside

// breakNoSingleflight and breakNoInvalidateGuard switch off stampede
// protection and the invalidation guard when the package is built with
// -tags breakit (see breakit_on.go). Defaults: off.
const (
	breakNoSingleflight    = false
	breakNoInvalidateGuard = false
)
