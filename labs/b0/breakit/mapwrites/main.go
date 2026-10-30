//go:build breakit

// Command mapwrites shows the runtime's built-in (best-effort) detection of
// unsynchronised map writes, WITHOUT the race detector.
//
//	go run -tags breakit ./labs/b0/breakit/mapwrites
//
// Expect "fatal error: concurrent map writes" (sometimes "concurrent map read
// and map write"). It is a fatal error, not a panic: recover() cannot catch
// it, and the whole process dies. Picture this in the SSO adapter's token
// cache at 3 a.m. Fix: a mutex around the map, or the safemap lab.
package main

import (
	"fmt"
	"sync"
)

func main() {
	tokens := map[int]string{} // BUG: shared, unguarded
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1_000_000; i++ {
				tokens[i%1024] = "tok"
			}
		}()
	}
	wg.Wait()
	fmt.Println("survived with", len(tokens), "entries (rare; run it again)")
}
