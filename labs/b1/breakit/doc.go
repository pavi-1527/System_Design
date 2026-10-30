//go:build breakit

// Package breakit holds intentionally broken Go design. Every file is behind
// the `breakit` build tag, so `go test ./...`, `go vet ./...` and `sd lab B1`
// never see it. Run the exercises explicitly; the commands and the output you
// should expect are in labs/b1/README.md.
//
//	go test -tags breakit -run TestTypedNilError ./labs/b1/breakit/
//
// Each file's comment says what is wrong. Your job: predict what Go will do,
// run it, read the report, then fix the code (in place, or in a copy) until the
// test passes.
package breakit
