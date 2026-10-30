//go:build breakit

// Command deadlock shows the runtime's global deadlock detector.
//
//	go run -tags breakit ./labs/b0/breakit/deadlock
//
// The producer forgets to close the channel, so `range` waits forever for a
// value that never comes. Every goroutine is blocked, the runtime notices and
// dies with "fatal error: all goroutines are asleep - deadlock!" and a stack
// showing goroutine 1 in [chan receive].
//
// Note: the detector only fires when EVERY goroutine is blocked. In a real
// server (an HTTP listener, a ticker, a signal.Notify goroutine...) the same
// bug is a silent hang, not a crash. Fix: the producer closes ch when done.
package main

import "fmt"

func main() {
	ch := make(chan int)
	go func() {
		for i := 0; i < 3; i++ {
			ch <- i
		}
		// BUG: missing close(ch)
	}()
	for v := range ch {
		fmt.Println("got", v)
	}
	fmt.Println("done") // never printed
}
