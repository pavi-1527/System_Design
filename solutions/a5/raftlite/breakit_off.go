//go:build !breakit

package raftlite

// breakSkipLogCheck disables the "candidate's log must be at least as up to
// date as ours" test in the vote handler when the package is built with
// -tags breakit (see breakit_on.go). Default: off.
const breakSkipLogCheck = false
