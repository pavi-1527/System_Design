package transcode

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

var tRenditions = []Rendition{{Name: "1080p", BitrateKbps: 5000}, {Name: "720p", BitrateKbps: 2500}, {Name: "360p", BitrateKbps: 800}}

func tJob(segments int) Job {
	return Job{ID: "vid_42", Segments: segments, Renditions: tRenditions}
}

// tWorker is a configurable fake worker.
type tWorker struct {
	mu        sync.Mutex
	calls     map[string]int // key → attempts
	inFlight  atomic.Int64
	maxSeen   atomic.Int64
	failUntil map[string]int  // key → fail attempts 1..n
	hangOnce  map[string]bool // key → first attempt blocks until ctx is done
	panicOn   map[string]int  // key → attempt number that panics
	delay     time.Duration
	order     []string // keys in completion order
}

func tNewWorker() *tWorker {
	return &tWorker{calls: map[string]int{}, failUntil: map[string]int{}, hangOnce: map[string]bool{}, panicOn: map[string]int{}}
}

func (w *tWorker) Worker(ctx context.Context, step Step, inputs []Artifact) (Artifact, error) {
	n := w.inFlight.Add(1)
	defer w.inFlight.Add(-1)
	for {
		m := w.maxSeen.Load()
		if n <= m || w.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	w.mu.Lock()
	w.calls[step.Key()]++
	attempt := w.calls[step.Key()]
	fail := attempt <= w.failUntil[step.Key()]
	hang := w.hangOnce[step.Key()] && attempt == 1
	pan := w.panicOn[step.Key()] == attempt
	w.mu.Unlock()
	if pan {
		panic("ffmpeg segfault")
	}
	if hang {
		<-ctx.Done()
		return Artifact{}, ctx.Err()
	}
	if w.delay > 0 {
		select {
		case <-time.After(w.delay):
		case <-ctx.Done():
			return Artifact{}, ctx.Err()
		}
	}
	if fail {
		return Artifact{}, fmt.Errorf("transient: %s attempt %d", step.Key(), attempt)
	}
	// Inputs must be the dependencies' artifacts, in order.
	var sb strings.Builder
	sb.WriteString(step.Kind.String())
	for _, in := range inputs {
		sb.WriteString("<" + in.Key + ">")
	}
	w.mu.Lock()
	w.order = append(w.order, step.Key())
	w.mu.Unlock()
	return Artifact{Data: []byte(sb.String())}, nil
}

func (w *tWorker) attempts(key string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls[key]
}

func (w *tWorker) totalCalls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, c := range w.calls {
		n += c
	}
	return n
}

// tSleep records backoff delays instead of sleeping.
type tSleep struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (s *tSleep) Sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.delays = append(s.delays, d)
	s.mu.Unlock()
	return ctx.Err()
}

func tRun(t *testing.T, ctx context.Context, cfg Config, job Job) (Result, error) {
	t.Helper()
	type out struct {
		r   Result
		err error
	}
	ch := make(chan out, 1)
	go func() {
		r, err := Run(ctx, cfg, job)
		ch <- out{r, err}
	}()
	select {
	case o := <-ch:
		return o.r, o.err
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return within 20 s (deadlock, or a step never scheduled?)")
		return Result{}, nil
	}
}

func tNoLeak(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutines leaked: %d before Run, %d after", before, runtime.NumGoroutine())
}

// ---------- the DAG ----------

func TestSteps_EnumeratesTranscodePackageManifest(t *testing.T) {
	steps, err := Steps(tJob(4))
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 4*3+4+1 {
		t.Fatalf("Steps(4 segments × 3 renditions) = %d steps, want 17", len(steps))
	}
	keys := map[string]bool{}
	for _, s := range steps {
		keys[s.Key()] = true
	}
	for _, want := range []string{"vid_42/transcode/0/1080p", "vid_42/transcode/3/360p", "vid_42/package/2", "vid_42/manifest"} {
		if !keys[want] {
			t.Errorf("missing step key %s", want)
		}
	}
	bad := []Job{{ID: "", Segments: 1, Renditions: tRenditions}, {ID: "x", Segments: 0, Renditions: tRenditions}, {ID: "x", Segments: 1},
		{ID: "x", Segments: 1, Renditions: []Rendition{{Name: "a"}, {Name: "a"}}}}
	for i, j := range bad {
		if _, err := Steps(j); !errors.Is(err, ErrInvalid) {
			t.Errorf("job %d: Steps = %v, want ErrInvalid", i, err)
		}
	}
	if _, err := Run(context.Background(), Config{Worker: tNewWorker().Worker}, Job{ID: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Run(invalid job): %v, want ErrInvalid", err)
	}
	if _, err := Run(context.Background(), Config{}, tJob(1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Run without a worker: %v, want ErrInvalid", err)
	}
}

func TestRun_RespectsDependenciesAndPassesInputsInOrder(t *testing.T) {
	w := tNewWorker()
	store := NewMemStore()
	res, err := tRun(t, context.Background(), Config{Worker: w.Worker, Store: store, MaxWorkers: 4}, tJob(3))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Steps != 13 || res.Executed != 13 || res.Skipped != 0 {
		t.Fatalf("Result = %+v, want 13 steps all executed", res)
	}
	wantManifest := "manifest<vid_42/package/0><vid_42/package/1><vid_42/package/2>"
	if string(res.Manifest.Data) != wantManifest || res.Manifest.Key != "vid_42/manifest" {
		t.Fatalf("manifest artifact = %q (key %q), want %q: inputs must be the package artifacts in segment order", res.Manifest.Data, res.Manifest.Key, wantManifest)
	}
	pkg1, ok := store.Get("vid_42/package/1")
	if !ok || string(pkg1.Data) != "package<vid_42/transcode/1/1080p><vid_42/transcode/1/720p><vid_42/transcode/1/360p>" {
		t.Fatalf("package/1 artifact = %q (found=%v): inputs must be the segment's renditions in job order", pkg1.Data, ok)
	}
	if store.Len() != 13 {
		t.Fatalf("store holds %d artifacts, want 13 (every output is persisted)", store.Len())
	}
	// Completion order: every package after its renditions, manifest last.
	pos := map[string]int{}
	for i, k := range w.order {
		pos[k] = i
	}
	for seg := 0; seg < 3; seg++ {
		for _, r := range tRenditions {
			if pos[fmt.Sprintf("vid_42/transcode/%d/%s", seg, r.Name)] > pos[fmt.Sprintf("vid_42/package/%d", seg)] {
				t.Fatalf("package/%d completed before transcode/%d/%s", seg, seg, r.Name)
			}
		}
		if pos[fmt.Sprintf("vid_42/package/%d", seg)] > pos["vid_42/manifest"] {
			t.Fatalf("manifest completed before package/%d", seg)
		}
	}
}

// ---------- concurrency ----------

func TestRun_BoundsConcurrencyAndOverlapsIndependentWork(t *testing.T) {
	w := tNewWorker()
	w.delay = 20 * time.Millisecond
	before := runtime.NumGoroutine()
	start := time.Now()
	res, err := tRun(t, context.Background(), Config{Worker: w.Worker, MaxWorkers: 6}, tJob(6)) // 18 transcodes + 6 packages + 1
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)
	if got := w.maxSeen.Load(); got != 6 {
		t.Fatalf("max in-flight steps = %d, want exactly 6 (MaxWorkers must be a bound and must be used)", got)
	}
	// Serial would be 25 × 20 ms = 500 ms; six-wide should be well under half.
	if elapsed > 300*time.Millisecond {
		t.Fatalf("25 steps of 20 ms with 6 workers took %v; independent steps must overlap", elapsed)
	}
	if res.Executed != 25 {
		t.Fatalf("Executed = %d, want 25", res.Executed)
	}
	tNoLeak(t, before)
	w2 := tNewWorker()
	if _, err := tRun(t, context.Background(), Config{Worker: w2.Worker}, tJob(2)); err != nil {
		t.Fatalf("Run with default MaxWorkers: %v", err)
	}
	if got := w2.maxSeen.Load(); got != 1 {
		t.Fatalf("default MaxWorkers ran %d steps at once, want 1", got)
	}
}

// ---------- retries, timeouts, panics ----------

func TestRun_RetriesTransientFailuresWithBackoff(t *testing.T) {
	w := tNewWorker()
	w.failUntil["vid_42/transcode/1/720p"] = 2 // fails twice, succeeds on the third attempt
	sl := &tSleep{}
	var progress []Progress
	cfg := Config{Worker: w.Worker, MaxWorkers: 2, MaxAttempts: 3,
		Backoff:    func(n int) time.Duration { return time.Duration(n) * time.Second },
		Sleep:      sl.Sleep,
		OnProgress: func(p Progress) { progress = append(progress, p) },
	}
	res, err := tRun(t, context.Background(), cfg, tJob(2))
	if err != nil {
		t.Fatalf("Run: %v (a transient failure within MaxAttempts must not fail the job)", err)
	}
	if n := w.attempts("vid_42/transcode/1/720p"); n != 3 {
		t.Fatalf("flaky step attempted %d times, want 3", n)
	}
	if len(sl.delays) != 2 || sl.delays[0] != time.Second || sl.delays[1] != 2*time.Second {
		t.Fatalf("backoff delays = %v, want [1s 2s] (Backoff(1), Backoff(2) between the three attempts)", sl.delays)
	}
	if res.Executed != 9 {
		t.Fatalf("Executed = %d, want 9", res.Executed)
	}
	if len(progress) != 9 || progress[8].Done != 9 || progress[8].Attempts != 11 || progress[8].Total != 9 {
		t.Fatalf("progress reports = %d, last = %+v; want 9 reports ending Done 9 / Attempts 11 / Total 9", len(progress), progress[len(progress)-1])
	}
	for i := 1; i < len(progress); i++ {
		if progress[i].Done != progress[i-1].Done+1 {
			t.Fatalf("progress is not monotonic: %+v then %+v", progress[i-1], progress[i])
		}
	}
}

func TestRun_FailsTheJobAfterMaxAttemptsAndStopsEverything(t *testing.T) {
	w := tNewWorker()
	w.delay = 5 * time.Millisecond
	w.failUntil["vid_42/transcode/0/360p"] = 100 // never succeeds
	before := runtime.NumGoroutine()
	res, err := tRun(t, context.Background(), Config{Worker: w.Worker, MaxWorkers: 3, MaxAttempts: 4, Sleep: (&tSleep{}).Sleep}, tJob(10))
	if !errors.Is(err, ErrStepFailed) {
		t.Fatalf("Run = %+v, %v; want ErrStepFailed", res, err)
	}
	if !strings.Contains(err.Error(), "vid_42/transcode/0/360p") || !strings.Contains(err.Error(), "attempt 4") {
		t.Fatalf("error %q should name the step and wrap the worker's last error", err)
	}
	if n := w.attempts("vid_42/transcode/0/360p"); n != 4 {
		t.Fatalf("failing step attempted %d times, want MaxAttempts = 4", n)
	}
	if total := w.totalCalls(); total > 4+30 {
		t.Fatalf("%d worker calls after the failure; the rest of the job must be cancelled, not run to completion (41 steps)", total)
	}
	if w.inFlight.Load() != 0 {
		t.Fatal("Run returned while worker calls were still in flight")
	}
	tNoLeak(t, before)
}

func TestRun_PerAttemptTimeoutRetriesAHungWorker(t *testing.T) {
	w := tNewWorker()
	w.hangOnce["vid_42/package/0"] = true
	res, err := tRun(t, context.Background(), Config{Worker: w.Worker, MaxWorkers: 2, MaxAttempts: 2, StepTimeout: 30 * time.Millisecond, Sleep: (&tSleep{}).Sleep}, tJob(1))
	if err != nil {
		t.Fatalf("Run: %v (a hung attempt must time out and be retried)", err)
	}
	if n := w.attempts("vid_42/package/0"); n != 2 {
		t.Fatalf("hung step attempted %d times, want 2", n)
	}
	if res.Executed != 5 {
		t.Fatalf("Executed = %d, want 5", res.Executed)
	}
}

func TestRun_ContainsWorkerPanics(t *testing.T) {
	w := tNewWorker()
	w.panicOn["vid_42/transcode/0/1080p"] = 1
	res, err := tRun(t, context.Background(), Config{Worker: w.Worker, MaxWorkers: 3, MaxAttempts: 2, Sleep: (&tSleep{}).Sleep}, tJob(2))
	if err != nil {
		t.Fatalf("Run: %v (a panicking attempt is a failed attempt, not a crashed pipeline)", err)
	}
	if n := w.attempts("vid_42/transcode/0/1080p"); n != 2 {
		t.Fatalf("panicking step attempted %d times, want 2", n)
	}
	if res.Executed != 9 {
		t.Fatalf("Executed = %d, want 9", res.Executed)
	}
	w2 := tNewWorker()
	w2.panicOn["vid_42/manifest"] = 1
	if _, err := tRun(t, context.Background(), Config{Worker: w2.Worker, MaxAttempts: 1}, tJob(1)); !errors.Is(err, ErrStepFailed) || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("Run with a step that always panics = %v; want ErrStepFailed mentioning the panic", err)
	}
}

// ---------- resume and cancellation ----------

func TestRun_ResumesFromTheStoreWithoutRedoingWork(t *testing.T) {
	store := NewMemStore()
	w := tNewWorker()
	w.failUntil["vid_42/package/3"] = 100
	_, err := tRun(t, context.Background(), Config{Worker: w.Worker, Store: store, MaxWorkers: 4, MaxAttempts: 2, Sleep: (&tSleep{}).Sleep}, tJob(4))
	if !errors.Is(err, ErrStepFailed) {
		t.Fatalf("first Run: %v, want ErrStepFailed", err)
	}
	persisted := store.Len()
	if persisted < 12 {
		t.Fatalf("only %d artifacts persisted before the failure; finished steps must be stored as they complete, not at the end", persisted)
	}
	w2 := tNewWorker() // the fixed worker
	res, err := tRun(t, context.Background(), Config{Worker: w2.Worker, Store: store, MaxWorkers: 4}, tJob(4))
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if res.Skipped != persisted || res.Executed != 17-persisted {
		t.Fatalf("resume: Skipped=%d Executed=%d, want Skipped=%d (what was stored) and Executed=%d", res.Skipped, res.Executed, persisted, 17-persisted)
	}
	if w2.totalCalls() != 17-persisted {
		t.Fatalf("worker called %d times on resume, want %d (never redo stored steps)", w2.totalCalls(), 17-persisted)
	}
	w3 := tNewWorker()
	res, err = tRun(t, context.Background(), Config{Worker: w3.Worker, Store: store}, tJob(4))
	if err != nil || res.Skipped != 17 || res.Executed != 0 || w3.totalCalls() != 0 {
		t.Fatalf("third Run (everything done) = %+v, %v, calls %d; want all 17 skipped and no worker calls", res, err, w3.totalCalls())
	}
	if string(res.Manifest.Data) == "" {
		t.Fatal("a fully skipped Run must still return the manifest artifact from the store")
	}
}

func TestRun_CancellationStopsPromptlyWithoutLeaks(t *testing.T) {
	w := tNewWorker()
	w.delay = 50 * time.Millisecond
	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, Config{Worker: w.Worker, MaxWorkers: 4}, tJob(50)) // 201 steps ≈ 2.5 s serially per worker
		done <- err
	}()
	time.Sleep(80 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run after cancel = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5 s of cancellation")
	}
	if w.totalCalls() >= 150 {
		t.Fatalf("%d worker calls after an early cancel; the pipeline kept scheduling", w.totalCalls())
	}
	if w.inFlight.Load() != 0 {
		t.Fatal("Run returned with worker calls still in flight")
	}
	tNoLeak(t, before)
}

func TestRun_ProgressCallbackMayCallBackIntoTheStore(t *testing.T) {
	store := NewMemStore()
	w := tNewWorker()
	var seen int
	cfg := Config{Worker: w.Worker, Store: store, MaxWorkers: 3, OnProgress: func(p Progress) {
		seen++
		_ = store.Len() // must not deadlock: the callback runs outside every lock
		if p.Done > p.Total || p.Executed+p.Skipped != p.Done {
			t.Errorf("inconsistent progress %+v", p)
		}
	}}
	if _, err := tRun(t, context.Background(), cfg, tJob(3)); err != nil {
		t.Fatal(err)
	}
	if seen != 13 {
		t.Fatalf("OnProgress called %d times, want once per step (13)", seen)
	}
}
