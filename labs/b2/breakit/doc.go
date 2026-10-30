//go:build breakit

// Package breakit holds intentionally broken pattern code. Every file is
// behind the `breakit` build tag, so `go test ./...`, `go vet ./...` and
// `sd lab B2` never see it. Run the exercises explicitly; the commands and
// the output you should expect are in labs/b2/README.md.
//
//	go test -tags breakit -run TestNaiveBus ./labs/b2/breakit/
//
// Each test's comment says what is wrong. Your job: predict what the test or
// program will report, run it, read the report, then fix the code (in place,
// or in a copy) until the test passes.
package breakit
