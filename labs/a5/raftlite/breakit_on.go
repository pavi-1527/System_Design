//go:build breakit

package raftlite

// Built with -tags breakit: your vote handler must treat every candidate's log
// as up to date (wire this constant into the log check, as the package doc
// describes). Then run
//
//	go test -tags breakit -run 'StaleLog' -v ./labs/a5/raftlite/
//
// and watch a committed entry get overwritten by a leader that never had it.
const breakSkipLogCheck = true
