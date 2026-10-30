//go:build breakit

package outbox

// Built with -tags breakit: Consumer.Handle commits the effect and the
// dedupe record in two separate transactions. Run
//
//	go test -tags breakit -run BreakIt -v ./labs/a6/outbox/
//
// and watch a crash between them apply a payment twice.
const breakSplitDedupe = true
