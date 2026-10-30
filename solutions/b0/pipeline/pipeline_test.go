package pipeline

import (
	"bytes"
	"context"
	"runtime"
	"runtime/pprof"
	"sort"
	"sync/atomic"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// collect drains c until it closes, failing the test if that takes too long
// (a stage that never closes its output hangs every `range` over it).
func collect[T any](t *testing.T, c <-chan T, what string) []T {
	t.Helper()
	var got []T
	deadline := time.After(testTimeout)
	for {
		select {
		case v, ok := <-c:
			if !ok {
				return got
			}
			got = append(got, v)
		case <-deadline:
			t.Fatalf("%s: output channel not closed within %v (got %d values so far)", what, testTimeout, len(got))
			return nil
		}
	}
}

func recvN[T any](t *testing.T, c <-chan T, n int, what string) []T {
	t.Helper()
	var got []T
	deadline := time.After(testTimeout)
	for len(got) < n {
		select {
		case v, ok := <-c:
			if !ok {
				t.Fatalf("%s: channel closed after %d values, wanted at least %d", what, len(got), n)
			}
			got = append(got, v)
		case <-deadline:
			t.Fatalf("%s: received only %d of %d values within %v", what, len(got), n, testTimeout)
		}
	}
	return got
}

func waitForGoroutines(t *testing.T, baseline int, what string) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			var buf bytes.Buffer
			_ = pprof.Lookup("goroutine").WriteTo(&buf, 1)
			t.Fatalf("%s: goroutine leak, %d running vs baseline %d. Live stacks (look for chan send/receive):\n%s",
				what, runtime.NumGoroutine(), baseline, buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func ints(n int) []int {
	s := make([]int, n)
	for i := range s {
		s[i] = i
	}
	return s
}

func square(_ context.Context, v int) int { return v * v }

// barrierFn returns an fn that blocks until `n` calls are inside it at the
// same time. A sequential implementation can never satisfy it.
func barrierFn(n int32, failed *atomic.Bool) func(context.Context, int) int {
	var inside atomic.Int32
	release := make(chan struct{})
	return func(ctx context.Context, v int) int {
		if inside.Add(1) == n {
			close(release)
		}
		select {
		case <-release:
		case <-ctx.Done():
		case <-time.After(testTimeout):
			failed.Store(true)
		}
		return v
	}
}

func TestGenerate_EmitsAllInOrderThenCloses(t *testing.T) {
	got := collect(t, Generate(context.Background(), 1, 2, 3, 4), "Generate")
	if len(got) != 4 || got[0] != 1 || got[3] != 4 {
		t.Fatalf("Generate emitted %v, want [1 2 3 4]", got)
	}
}

func TestMap_ProcessesEveryItem(t *testing.T) {
	ctx := context.Background()
	got := collect(t, Map(ctx, Generate(ctx, ints(1000)...), 8, square), "Map")
	if len(got) != 1000 {
		t.Fatalf("Map emitted %d values, want 1000", len(got))
	}
	sort.Ints(got)
	for i, v := range ints(1000) {
		if got[i] != v*v {
			t.Fatalf("missing or wrong value at sorted position %d: got %d, want %d", i, got[i], v*v)
		}
	}
}

func TestMap_RunsWorkersInParallel(t *testing.T) {
	ctx := context.Background()
	var failed atomic.Bool
	got := collect(t, Map(ctx, Generate(ctx, ints(8)...), 4, barrierFn(4, &failed)), "Map")
	if failed.Load() {
		t.Fatal("4 calls to fn were never in flight at the same time with workers=4: Map is not running workers concurrently")
	}
	if len(got) != 8 {
		t.Fatalf("got %d values, want 8", len(got))
	}
}

func TestMapOrdered_PreservesInputOrder(t *testing.T) {
	ctx := context.Background()
	slowEarly := func(_ context.Context, v int) int {
		// Earlier items take longer, so completion order != input order.
		time.Sleep(time.Duration((10-v%10)*100) * time.Microsecond)
		return v * 10
	}
	got := collect(t, MapOrdered(ctx, Generate(ctx, ints(200)...), 6, slowEarly), "MapOrdered")
	if len(got) != 200 {
		t.Fatalf("MapOrdered emitted %d values, want 200", len(got))
	}
	for i, v := range got {
		if v != i*10 {
			t.Fatalf("position %d holds %d, want %d: MapOrdered must emit results in input order", i, v, i*10)
		}
	}
}

func TestMapOrdered_RunsWorkersInParallel(t *testing.T) {
	ctx := context.Background()
	var failed atomic.Bool
	got := collect(t, MapOrdered(ctx, Generate(ctx, ints(8)...), 4, barrierFn(4, &failed)), "MapOrdered")
	if failed.Load() {
		t.Fatal("4 calls to fn were never in flight at the same time with workers=4: ordered output must not mean sequential processing")
	}
	for i, v := range got {
		if v != i {
			t.Fatalf("position %d holds %d, want %d", i, v, i)
		}
	}
	if len(got) != 8 {
		t.Fatalf("got %d values, want 8", len(got))
	}
}

func TestMerge_ForwardsEverythingThenCloses(t *testing.T) {
	ctx := context.Background()
	a := Generate(ctx, 1, 2, 3)
	b := Generate(ctx, 10, 20)
	c := Generate[int](ctx)
	got := collect(t, Merge(ctx, a, b, c), "Merge")
	sort.Ints(got)
	want := []int{1, 2, 3, 10, 20}
	if len(got) != len(want) {
		t.Fatalf("Merge emitted %v, want %v (any order)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Merge emitted %v, want %v (any order)", got, want)
		}
	}
	if got := collect(t, Merge[int](ctx), "Merge of zero inputs"); len(got) != 0 {
		t.Fatalf("Merge() emitted %v, want nothing", got)
	}
}

// The leak test: a consumer reads a few results, cancels, and walks away
// WITHOUT draining. Every goroutine in the pipeline must exit.
func TestPipeline_CancelMidStream_AbandonedConsumer_LeaksNothing(t *testing.T) {
	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	src := Generate(ctx, ints(100000)...)
	sq := Map(ctx, src, 4, square)
	ordered := MapOrdered(ctx, sq, 4, func(_ context.Context, v int) int { return v + 1 })
	other := Generate(ctx, ints(100000)...)
	merged := Merge(ctx, ordered, other)

	recvN(t, merged, 10, "merged pipeline")
	cancel()
	// Do NOT read `merged` again: that is the abandoned-consumer case.
	waitForGoroutines(t, baseline, "after cancel with an abandoned consumer")
}

func TestPipeline_CancelMidStream_OutputCloses(t *testing.T) {
	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	out := MapOrdered(ctx, Map(ctx, Generate(ctx, ints(100000)...), 3, square), 3, square)
	recvN(t, out, 5, "pipeline")
	cancel()
	rest := collect(t, out, "pipeline after cancel") // must terminate
	if len(rest) > 100000-5 {
		t.Fatalf("drained %d more values after cancel; the pipeline did not stop", len(rest))
	}
	waitForGoroutines(t, baseline, "after cancel and drain")
}

func TestMap_PassesContextToFn_SoBlockedCallsUnwind(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage func(context.Context, <-chan int, int, func(context.Context, int) int) <-chan int
	}{
		{"Map", Map[int, int]},
		{"MapOrdered", MapOrdered[int, int]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseline := runtime.NumGoroutine()
			ctx, cancel := context.WithCancel(context.Background())
			var entered atomic.Int32
			blocking := func(ctx context.Context, v int) int {
				entered.Add(1)
				<-ctx.Done() // a vendor call that only returns when cancelled
				return v
			}
			out := tc.stage(ctx, Generate(ctx, ints(10)...), 3, blocking)
			deadline := time.Now().Add(testTimeout)
			for entered.Load() < 3 {
				select {
				case _, ok := <-out:
					cancel()
					if !ok {
						t.Fatalf("%s closed its output before fn returned for any item", tc.name)
					}
					t.Fatalf("%s emitted a value while every fn call was still blocked", tc.name)
				default:
				}
				if time.Now().After(deadline) {
					cancel()
					t.Fatalf("only %d of 3 workers entered fn", entered.Load())
				}
				time.Sleep(time.Millisecond)
			}
			cancel()
			collect(t, out, tc.name+" after cancel")
			waitForGoroutines(t, baseline, tc.name+" with fn blocked on ctx")
		})
	}
}

func TestPipeline_StagesStopPromptlyOnPreCancelledContext(t *testing.T) {
	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	fn := func(_ context.Context, v int) int { calls.Add(1); return v }
	out := Merge(ctx, Map(ctx, Generate(ctx, ints(100000)...), 4, fn), MapOrdered(ctx, Generate(ctx, ints(100000)...), 4, fn))
	got := collect(t, out, "pipeline on a cancelled context")
	if len(got) > 1000 || calls.Load() > 1000 {
		t.Fatalf("on an already-cancelled context the pipeline still emitted %d values and called fn %d times; stages must check ctx", len(got), calls.Load())
	}
	waitForGoroutines(t, baseline, "pre-cancelled context")
}
