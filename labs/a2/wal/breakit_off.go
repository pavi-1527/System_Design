//go:build !breakit

package wal

// breakSkipTruncate makes Open leave a torn tail in the file instead of
// truncating it. It is true only when the package is built with -tags breakit
// (see breakit_on.go).
const breakSkipTruncate = false
