// Package transcode is the job pipeline behind "upload a video, watch it in
// any quality": a dependency graph of steps (transcode every segment into
// every rendition, package each segment once all its renditions exist, then
// write the manifest once every segment is packaged), run by a bounded pool
// of workers with retries, per-attempt timeouts, idempotent resume from a
// store of finished artifacts, panic containment, progress reporting and
// cancellation that leaks nothing.
//
// It is the lab for C11 Unit 2 §5–§6. Suggested order: the DAG (Steps and
// dependencies), a serial Run, then the worker pool, retries and timeouts,
// then resume, cancellation and panics. Run: go test -race ./labs/c11/transcode/
//
// Contract (the tests depend on every line):
//   - Steps(job) returns every step of a job: one StepTranscode per
//     (segment, rendition), one StepPackage per segment, one StepManifest.
//     Step.Key is "<job>/transcode/<segment>/<rendition>",
//     "<job>/package/<segment>" or "<job>/manifest". A job with no segments
//     or no renditions is ErrInvalid.
//   - Dependencies: package(s) needs transcode(s, r) for every rendition r;
//     manifest needs package(s) for every segment s. Run never starts a step
//     before every step it depends on has an artifact, and passes those
//     artifacts as inputs in a stable order (renditions in job order,
//     segments ascending).
//   - At most MaxWorkers steps run at the same time (default 1 if <= 0).
//     Steps with satisfied dependencies run as soon as a worker is free, so
//     independent segments overlap.
//   - A step whose Key is already in the Store is skipped: its artifact is
//     reused and the worker is not called. Every artifact a worker produces
//     is written to the Store under the step's Key before dependents start.
//     That makes Run idempotent: running the same job twice calls the worker
//     only for what is missing (Result.Executed vs Result.Skipped).
//   - A worker error is retried up to MaxAttempts attempts in total (default
//     3) with cfg.Sleep(ctx, cfg.Backoff(attempt)) between attempts, attempt
//     counting from 1. Each attempt runs under a context that expires after
//     StepTimeout (no timeout if <= 0); a timed-out attempt counts as a
//     failed attempt. A worker panic is recovered and treated as a failed
//     attempt. When a step's attempts are exhausted, Run cancels the rest of
//     the job, waits for running steps to return, and returns an error that
//     wraps ErrStepFailed and the worker's last error.
//   - When ctx is cancelled, Run stops starting steps, waits for running
//     steps to return, and returns ctx.Err(). No goroutine outlives Run.
//   - OnProgress, if set, is called from Run's own goroutine (never from a
//     worker, never under a lock) after every step that finishes or is
//     skipped, with Done counting both, so it may call back into the
//     pipeline's dependencies.
//   - Result reports the manifest artifact, the number of steps, how many
//     ran and how many were skipped. Nothing in this package starts a
//     goroutine except inside Run, and Run returns only after all of them
//     have exited.
package transcode

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Errors are values: compare with errors.Is.
var (
	ErrInvalid    = errors.New("transcode: invalid job")
	ErrStepFailed = errors.New("transcode: step failed after all attempts")
)

// Rendition is one output quality.
type Rendition struct {
	Name        string // e.g. "1080p"
	BitrateKbps int
}

// Job describes a source video to process.
type Job struct {
	ID         string
	Segments   int // the source is split into this many segments
	Renditions []Rendition
}

// StepKind is what a step does.
type StepKind int

// Step kinds.
const (
	StepTranscode StepKind = iota + 1
	StepPackage
	StepManifest
)

func (k StepKind) String() string {
	switch k {
	case StepTranscode:
		return "transcode"
	case StepPackage:
		return "package"
	case StepManifest:
		return "manifest"
	}
	return "invalid"
}

// Step is one unit of work.
type Step struct {
	Job       string
	Kind      StepKind
	Segment   int    // 0-based; -1 for the manifest
	Rendition string // transcode steps only
}

// Key is the step's identity in the Store.
func (s Step) Key() string {
	switch s.Kind {
	case StepTranscode:
		return fmt.Sprintf("%s/transcode/%d/%s", s.Job, s.Segment, s.Rendition)
	case StepPackage:
		return fmt.Sprintf("%s/package/%d", s.Job, s.Segment)
	default:
		return s.Job + "/manifest"
	}
}

// Artifact is a step's output. In the lab it is whatever bytes the fake
// worker produces; in production it is a path or object key plus metadata.
type Artifact struct {
	Key  string
	Data []byte
}

// Worker runs one step attempt. inputs are the artifacts the step depends
// on. It must respect ctx.
type Worker func(ctx context.Context, step Step, inputs []Artifact) (Artifact, error)

// Store holds finished artifacts by step key.
type Store interface {
	Get(key string) (Artifact, bool)
	Put(a Artifact)
}

// MemStore is an in-memory Store, safe for concurrent use.
type MemStore struct {
	mu sync.Mutex
	m  map[string]Artifact
}

// NewMemStore returns an empty store.
func NewMemStore() *MemStore { return &MemStore{m: map[string]Artifact{}} }

// Get implements Store.
func (s *MemStore) Get(key string) (Artifact, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.m[key]
	return a, ok
}

// Put implements Store.
func (s *MemStore) Put(a Artifact) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[a.Key] = a
}

// Len reports how many artifacts are stored.
func (s *MemStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

// Progress is a snapshot reported after every finished or skipped step.
type Progress struct {
	Total    int
	Done     int // executed + skipped
	Executed int
	Skipped  int
	Attempts int // worker calls so far, including failed ones
}

// Config configures Run. Zero values get defaults.
type Config struct {
	Worker      Worker
	Store       Store
	MaxWorkers  int
	MaxAttempts int
	StepTimeout time.Duration
	Backoff     func(attempt int) time.Duration                  // default: 100 ms × 2^(attempt-1)
	Sleep       func(ctx context.Context, d time.Duration) error // default: timer that honours ctx
	OnProgress  func(Progress)
}

// Result is what Run returns on success.
type Result struct {
	Manifest Artifact
	Steps    int
	Executed int
	Skipped  int
}

// Steps enumerates a job's steps in a deterministic order.
func Steps(job Job) ([]Step, error) {
	if job.ID == "" || job.Segments <= 0 || len(job.Renditions) == 0 {
		return nil, fmt.Errorf("%w: need an id, at least one segment and one rendition", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, r := range job.Renditions {
		if r.Name == "" || seen[r.Name] {
			return nil, fmt.Errorf("%w: rendition names must be unique and non-empty", ErrInvalid)
		}
		seen[r.Name] = true
	}
	var steps []Step
	for s := 0; s < job.Segments; s++ {
		for _, r := range job.Renditions {
			steps = append(steps, Step{Job: job.ID, Kind: StepTranscode, Segment: s, Rendition: r.Name})
		}
	}
	for s := 0; s < job.Segments; s++ {
		steps = append(steps, Step{Job: job.ID, Kind: StepPackage, Segment: s})
	}
	steps = append(steps, Step{Job: job.ID, Kind: StepManifest, Segment: -1})
	return steps, nil
}

// deps returns the keys a step depends on, in input order.
func deps(job Job, s Step) []string {
	switch s.Kind {
	case StepTranscode:
		return nil
	case StepPackage:
		out := make([]string, 0, len(job.Renditions))
		for _, r := range job.Renditions {
			out = append(out, Step{Job: job.ID, Kind: StepTranscode, Segment: s.Segment, Rendition: r.Name}.Key())
		}
		return out
	default:
		out := make([]string, 0, job.Segments)
		for seg := 0; seg < job.Segments; seg++ {
			out = append(out, Step{Job: job.ID, Kind: StepPackage, Segment: seg}.Key())
		}
		return out
	}
}

func defaultSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func defaultBackoff(attempt int) time.Duration {
	d := 100 * time.Millisecond
	for i := 1; i < attempt; i++ {
		d *= 2
	}
	return d
}

type stepResult struct {
	step     Step
	artifact Artifact
	attempts int
	err      error
}

// attempt runs one worker call with timeout and panic containment.
func attempt(ctx context.Context, cfg Config, step Step, inputs []Artifact) (a Artifact, err error) {
	actx := ctx
	if cfg.StepTimeout > 0 {
		var cancel context.CancelFunc
		actx, cancel = context.WithTimeout(ctx, cfg.StepTimeout)
		defer cancel()
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("worker panicked on %s: %v", step.Key(), r)
		}
	}()
	return cfg.Worker(actx, step, inputs)
}

// runStep executes a step with retries. It returns the result and how many
// attempts it made.
func runStep(ctx context.Context, cfg Config, step Step, inputs []Artifact) (Artifact, int, error) {
	var last error
	for n := 1; n <= cfg.MaxAttempts; n++ {
		if ctx.Err() != nil {
			return Artifact{}, n - 1, ctx.Err()
		}
		a, err := attempt(ctx, cfg, step, inputs)
		if err == nil {
			a.Key = step.Key()
			return a, n, nil
		}
		last = err
		if n == cfg.MaxAttempts {
			break
		}
		if err := cfg.Sleep(ctx, cfg.Backoff(n)); err != nil {
			return Artifact{}, n, err
		}
	}
	return Artifact{}, cfg.MaxAttempts, last
}

// Run executes a job's steps respecting dependencies, concurrency, retries
// and cancellation. See the package doc for the contract.
func Run(ctx context.Context, cfg Config, job Job) (Result, error) {
	steps, err := Steps(job)
	if err != nil {
		return Result{}, err
	}
	if cfg.Worker == nil {
		return Result{}, fmt.Errorf("%w: a Worker is required", ErrInvalid)
	}
	if cfg.Store == nil {
		cfg.Store = NewMemStore()
	}
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = 1
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.Backoff == nil {
		cfg.Backoff = defaultBackoff
	}
	if cfg.Sleep == nil {
		cfg.Sleep = defaultSleep
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Dependency bookkeeping.
	remaining := map[string]int{}      // key → unfinished dependencies
	dependents := map[string][]Step{}  // key → steps waiting on it
	artifacts := map[string]Artifact{} // finished outputs
	byKey := map[string]Step{}
	for _, s := range steps {
		byKey[s.Key()] = s
		d := deps(job, s)
		remaining[s.Key()] = len(d)
		for _, dk := range d {
			dependents[dk] = append(dependents[dk], s)
		}
	}

	prog := Progress{Total: len(steps)}
	report := func() {
		if cfg.OnProgress != nil {
			cfg.OnProgress(prog)
		}
	}

	var ready []Step
	for _, s := range steps {
		if remaining[s.Key()] == 0 {
			ready = append(ready, s)
		}
	}

	// Worker pool.
	type task struct {
		step   Step
		inputs []Artifact
	}
	tasks := make(chan task)
	results := make(chan stepResult)
	var wg sync.WaitGroup
	for i := 0; i < cfg.MaxWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case t, ok := <-tasks:
					if !ok {
						return
					}
					a, n, err := runStep(ctx, cfg, t.step, t.inputs)
					select {
					case results <- stepResult{step: t.step, artifact: a, attempts: n, err: err}:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	// finish makes the pool exit and waits for it. Safe to call once.
	finish := func() {
		cancel()
		close(tasks)
		wg.Wait()
	}

	inFlight := 0
	finished := func(s Step, a Artifact) {
		artifacts[s.Key()] = a
		for _, d := range dependents[s.Key()] {
			remaining[d.Key()]--
			if remaining[d.Key()] == 0 {
				ready = append(ready, d)
			}
		}
		prog.Done++
	}
	inputsFor := func(s Step) []Artifact {
		d := deps(job, s)
		in := make([]Artifact, 0, len(d))
		for _, k := range d {
			in = append(in, artifacts[k])
		}
		return in
	}
	result := Result{Steps: len(steps)}
	for prog.Done < len(steps) {
		// Skip anything the store already has, without a worker.
		for len(ready) > 0 {
			s := ready[0]
			if a, ok := cfg.Store.Get(s.Key()); ok {
				ready = ready[1:]
				prog.Skipped++
				result.Skipped++
				finished(s, a)
				report()
				continue
			}
			break
		}
		if prog.Done >= len(steps) {
			break // everything was skipped: nothing is in flight, so no result will ever arrive
		}
		var send chan task
		var next task
		if len(ready) > 0 && inFlight < cfg.MaxWorkers {
			next = task{step: ready[0], inputs: inputsFor(ready[0])}
			send = tasks
		}
		if send == nil && inFlight == 0 && len(ready) == 0 && prog.Done < len(steps) {
			finish()
			return Result{}, fmt.Errorf("%w: no runnable steps (dependency cycle?)", ErrInvalid)
		}
		select {
		case <-ctx.Done():
			finish()
			return Result{}, ctx.Err()
		case send <- next:
			ready = ready[1:]
			inFlight++
		case r := <-results:
			inFlight--
			prog.Attempts += r.attempts
			if r.err != nil {
				finish()
				if ctx.Err() != nil && errors.Is(r.err, ctx.Err()) {
					return Result{}, ctx.Err()
				}
				return Result{}, fmt.Errorf("%w: %s after %d attempts: %w", ErrStepFailed, r.step.Key(), r.attempts, r.err)
			}
			cfg.Store.Put(r.artifact)
			prog.Executed++
			result.Executed++
			finished(r.step, r.artifact)
			report()
		}
	}
	finish()
	result.Manifest = artifacts[Step{Job: job.ID, Kind: StepManifest, Segment: -1}.Key()]
	return result, nil
}
