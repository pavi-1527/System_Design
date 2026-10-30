//go:build breakit

package histogram

// Built with -tags breakit: the Sketch's lock()/unlock() become no-ops. Run
//
//	go test -tags breakit -race -run Concurrent -v ./labs/a8/histogram/
//
// and watch the race detector (or the runtime's "concurrent map writes"
// check) fire.
const breakNoLock = true
