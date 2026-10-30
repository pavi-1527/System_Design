//go:build !breakit

package lsm

// breakAlwaysDropTombstones makes compaction drop every tombstone it meets,
// even when older data may still exist in deeper levels. It is true only when
// the package is built with -tags breakit (see breakit_on.go).
const breakAlwaysDropTombstones = false
