//go:build breakit

package breakit

import (
	"bytes"
	"runtime"
	"runtime/pprof"
	"sync"
	"testing"
	"time"
)

// 1. Lost updates. hits++ is a read, an add and a write; two goroutines can
// read the same old value. -race reports "WARNING: DATA RACE" with both
// stacks. Without -race the count is usually (not always) short.
// Fix: sync/atomic (atomic.Int64) or a mutex.
func TestRacyCounter(t *testing.T) {
	var hits int
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				hits++ // BUG: unsynchronised read-modify-write
			}
		}()
	}
	wg.Wait()
	if hits != 100*1000 {
		t.Errorf("hits = %d, want %d: %d increments lost", hits, 100*1000, 100*1000-hits)
	}
}

type config struct{ vendorURL string }

var (
	cfg   *config
	loads int
)

func loadConfig() *config {
	loads++ // pretend this reads a file / calls a secrets manager
	return &config{vendorURL: "https://vendor.example"}
}

// 2. Check-then-act lazy init. Several goroutines see cfg == nil and all
// "initialise" it; the pointer write races with the reads. -race reports it
// even in runs where loads happens to be 1.
// Fix: sync.Once (or sync.OnceValue, Go 1.21+).
func getConfig() *config {
	if cfg == nil { // BUG: unsynchronised read
		cfg = loadConfig() // BUG: unsynchronised write
	}
	return cfg
}

func TestRacyLazyInit(t *testing.T) {
	cfg, loads = nil, 0
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if getConfig().vendorURL == "" {
				t.Error("empty config")
			}
		}()
	}
	wg.Wait()
	if loads != 1 {
		t.Errorf("config loaded %d times, want 1", loads)
	}
}

// 3. WaitGroup.Add inside the goroutine. Wait can run before some goroutines
// have called Add, see a zero counter and return early; then `total` is read
// while late goroutines still write it. -race reports a race on total; the
// sum is sometimes short. (Go 1.25's vet adds a `waitgroup` analyzer for
// this pattern.)
// Fix: wg.Add(1) before the go statement.
func TestWaitGroupAddInsideGoroutine(t *testing.T) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	total := 0
	for i := 0; i < 100; i++ {
		go func() {
			wg.Add(1) // BUG: too late; Wait may already have returned
			defer wg.Done()
			mu.Lock()
			total++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if total != 100 { // BUG: read without the mutex, racing with late goroutines
		t.Errorf("total = %d after Wait, want 100: Wait returned before all work started", total)
	}
	time.Sleep(10 * time.Millisecond) // let stragglers finish so they don't pollute the next test
}

// firstResponse asks every vendor and returns the first answer.
// BUG: the channel is unbuffered, so every vendor except the winner blocks
// forever on `ch <- v()` once we have returned: one leaked goroutine per
// losing vendor per call. At 200 req/s with 3 vendors that is 400
// goroutines/s, ≈ 35 M/day; each pins its stack and whatever it references.
// Fix: make(chan string, len(vendors)), or select on a ctx.Done() in the sender.
func firstResponse(vendors []func() string) string {
	ch := make(chan string)
	for _, v := range vendors {
		go func() { ch <- v() }()
	}
	return <-ch
}

// 4. Goroutine leak. The test counts goroutines before and after, with a
// settle loop, and prints the goroutine profile so you can see WHERE they
// are stuck ("chan send" in firstResponse.func1).
func TestLeakyFirstResponse(t *testing.T) {
	baseline := runtime.NumGoroutine()
	vendors := []func() string{
		func() string { return "a" },
		func() string { time.Sleep(time.Millisecond); return "b" },
		func() string { time.Sleep(2 * time.Millisecond); return "c" },
	}
	for i := 0; i < 100; i++ {
		_ = firstResponse(vendors)
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > baseline {
		var buf bytes.Buffer
		_ = pprof.Lookup("goroutine").WriteTo(&buf, 1)
		t.Errorf("leaked %d goroutines after 100 calls (baseline %d, now %d). Goroutine profile:\n%s", n-baseline, baseline, n, buf.String())
	}
}

// 5. Closing a channel twice: two "owners" both think they should close.
// Runtime panic: "panic: close of closed channel". A panic ends the whole
// test binary, so this test is last in the file; run it on its own with
// -run TestDoubleClose.
// Fix: exactly one owner closes (the sender side), or guard with sync.Once.
func TestDoubleClose(t *testing.T) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	shutdown := func() {
		defer wg.Done()
		close(done) // BUG: both the signal handler and the deploy hook call this
	}
	wg.Add(2)
	go shutdown()
	go shutdown()
	wg.Wait()
	<-done
}
