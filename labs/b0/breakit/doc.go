//go:build breakit

// Package breakit holds intentionally broken concurrent code. Every file is
// behind the `breakit` build tag, so `go test ./...`, `go vet ./...` and
// `sd lab B0` never see it. Run the exercises explicitly; the commands and
// the output you should expect are in labs/b0/README.md.
//
//	go test -race -tags breakit -run TestRacyCounter ./labs/b0/breakit/
//
// Each test's comment says what is wrong. Your job: predict what the tool
// will report, run it, read the report, then fix the code (in place, or in a
// copy) until the test passes under -race.
package breakit
