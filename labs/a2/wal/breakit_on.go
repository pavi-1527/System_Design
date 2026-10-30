//go:build breakit

package wal

// Built with -tags breakit: Open does not truncate a torn tail. Run
//
//	go test -tags breakit -run Crash -v ./labs/a2/wal/
//
// and watch records appended after a crash get buried behind garbage.
const breakSkipTruncate = true
