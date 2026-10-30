//go:build breakit

package lsm

// Built with -tags breakit: compaction passes dropTombstones=true to Merge
// unconditionally. Run
//
//	go test -tags breakit -run Tombstone -v ./labs/a2/lsm/
//
// and watch deleted keys come back to life.
const breakAlwaysDropTombstones = true
