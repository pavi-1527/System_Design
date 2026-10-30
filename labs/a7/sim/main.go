//go:build breakit

// Command sim holds the small simulations behind A7's "Break it" sections.
// Everything is deterministic (fixed seeds, virtual time) except `timeouts`,
// which uses millisecond-scale real time. It uses only the standard library
// and none of your lab code, so it runs before any lab is finished:
//
//	go run -tags breakit ./labs/a7/sim backoff
//	go run -tags breakit ./labs/a7/sim timeouts [-propagate]
//	go run -tags breakit ./labs/a7/sim metastable [-mode naive|noretry|budget|deadline|lifo|shed]
//	go run -tags breakit ./labs/a7/sim hedge [-corr 0]
//	go run -tags breakit ./labs/a7/sim distlimit
//	go run -tags breakit ./labs/a7/sim availability
//
// Each subcommand prints the table quoted in the unit that uses it.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "backoff":
		backoff(args)
	case "timeouts":
		timeouts(args)
	case "metastable":
		metastable(args)
	case "hedge":
		hedge(args)
	case "distlimit":
		distlimit(args)
	case "availability":
		availability(args)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: sim backoff|timeouts|metastable|hedge|distlimit|availability [flags]")
	os.Exit(2)
}
