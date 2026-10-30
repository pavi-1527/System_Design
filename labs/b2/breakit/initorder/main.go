//go:build breakit

// Command initorder shows the init-order surprise behind registries.
//
//	go run -tags breakit ./labs/b2/breakit/initorder
//
// A registry is filled by init() functions (as database/sql drivers are). The
// package-level variable below looks the default provider up while the
// package's variables are being initialised, and Go initialises ALL of a
// package's variables before it runs ANY of its init() functions. So the
// lookup happens first, finds an empty registry and panics before main runs.
//
// Predict the panic message and the line it blames, run it, then fix it in two
// different ways: (1) look the provider up lazily, inside main or a function;
// (2) make registration explicit (a Register call from main) so no
// ordering rule is needed. Which one would you choose for a registry shared by
// several packages, and why?
//
// Across packages the rule is different and just as easy to trip over: an
// imported package is fully initialised before its importer, but the order of
// UNRELATED packages follows the Go spec's import-path ordering (Go 1.21+), so
// code must never depend on "driver A registered before driver B".
package main

import "fmt"

var registry = map[string]func() string{}

func register(name string, f func() string) { registry[name] = f }

func mustGet(name string) func() string {
	f, ok := registry[name]
	if !ok {
		panic(fmt.Sprintf("unknown provider %q (registered: %d)", name, len(registry)))
	}
	return f
}

var defaultProvider = mustGet("acme") // BUG: runs before init() below

func init() {
	register("acme", func() string { return "acme says hello" })
}

func main() {
	fmt.Println(defaultProvider())
}
