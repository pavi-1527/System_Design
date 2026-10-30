//go:build breakit && go1.21

// Command loopvar shows the pre-Go 1.22 loop-variable semantics.
//
//	go run -tags breakit ./labs/b0/breakit/loopvar
//
// The `go1.21` term in the build constraint sets THIS FILE's language version
// to Go 1.21, so the loop variable is one variable shared by all iterations
// and every closure sees its final value. Predict the output first. Then
// delete `&& go1.21` from the constraint (the file now uses the module's
// go 1.22 semantics: a fresh variable per iteration) and run it again.
package main

import "fmt"

func main() {
	var printers []func()
	for i := 0; i < 3; i++ {
		printers = append(printers, func() { fmt.Print(i, " ") })
	}
	for _, p := range printers {
		p()
	}
	fmt.Println()
}
