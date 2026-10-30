//go:build breakit

package idempotency

// Built with -tags breakit: the middleware replaces the atomic Reserve with a
// non-atomic Get-then-run ("check then act"). Run
//
//	go test -tags breakit -race -run Concurrent -v ./labs/a1/idempotency/
//
// and watch the duplicate-charge test fail.
const breakNoInFlightLock = true
