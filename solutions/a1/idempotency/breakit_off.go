//go:build !breakit

package idempotency

// breakNoInFlightLock disables the atomic in-flight reservation when the
// package is built with -tags breakit (see breakit_on.go). Default: off.
const breakNoInFlightLock = false
