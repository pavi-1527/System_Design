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
//
// Hints:
//   - Keep the dependency bookkeeping in Run's goroutine: a count of
//     unfinished dependencies per step and a list of dependents per step.
//     Workers only run steps and send results back on a channel; the
//     scheduler decides what becomes ready. Then no lock is needed and
//     OnProgress trivially runs outside any lock.
//   - Use the nil-channel trick in the scheduler's select: only enable the
//     "send a task" case when something is ready and a worker is free.
//   - Check the store for each step at the moment it becomes ready, and if
//     everything was skipped make sure you leave the loop instead of waiting
//     for a result that will never come.
//   - Wrap each worker call in a function that recovers panics and applies
//     the per-attempt timeout with context.WithTimeout.
//   - To stop: cancel the context, close the task channel, wait for the
//     worker WaitGroup. Workers must select on ctx.Done() when sending
//     results, or a cancelled Run deadlocks.
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

var errNotImplemented = errors.New("transcode: not implemented")

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

// String is GIVEN.
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

// Key is the step's identity in the Store. GIVEN.
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

// MemStore is an in-memory Store, safe for concurrent use. GIVEN.
type MemStore struct {
	mu sync.Mutex
	m  map[string]Artifact
}

// NewMemStore returns an empty store. GIVEN.
func NewMemStore() *MemStore { return &MemStore{m: map[string]Artifact{}} }

// Get implements Store. GIVEN.
func (s *MemStore) Get(key string) (Artifact, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.m[key]
	return a, ok
}

// Put implements Store. GIVEN.
func (s *MemStore) Put(a Artifact) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[a.Key] = a
}

// Len reports how many artifacts are stored. GIVEN.
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

// Steps enumerates a job's steps in a deterministic order: every transcode
// step (segment-major, renditions in job order), then every package step,
// then the manifest.
func Steps(job Job) ([]Step, error) {
	// TODO(lab): validate the job (id, segments, unique non-empty
	// rendition names) and build the list.
	_ = job
	return nil, errNotImplemented
}

// Run executes a job's steps respecting dependencies, concurrency, retries
// and cancellation. See the package doc for the contract.
func Run(ctx context.Context, cfg Config, job Job) (Result, error) {
	// TODO(lab)
	_, _, _ = ctx, cfg, job
	return Result{}, errNotImplemented
}
