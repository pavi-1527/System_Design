//go:build !breakit

package outbox

// breakSplitDedupe makes Consumer.Handle apply the effect and record the
// dedupe row in two separate transactions when the package is built with
// -tags breakit (see breakit_on.go). Default: off.
const breakSplitDedupe = false
