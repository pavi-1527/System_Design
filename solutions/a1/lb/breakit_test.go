//go:build breakit

package lb

// Break-it experiments for A1 Unit 1 §4–§5. These are measurements, not
// pass/fail tests: they print a table so you can compare strategies.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a1/lb/

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestBreakIt_SlowBackendTail: 4 backends, one of them is slow (think: GC
// pause, a noisy neighbour, a vendor adapter whose downstream is degraded).
// Compare how much traffic each picker sends to it and what that does to the
// latency distribution clients see.
func TestBreakIt_SlowBackendTail(t *testing.T) {
	const (
		fast        = 5 * time.Millisecond
		slow        = 80 * time.Millisecond
		clients     = 16
		perClient   = 40
		slowBackend = 3
	)
	var bs []*Backend
	var hits [4]atomic.Int64
	for i := 0; i < 4; i++ {
		i := i
		d := fast
		if i == slowBackend {
			d = slow
		}
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits[i].Add(1)
			time.Sleep(d)
			io.WriteString(w, "ok")
		}))
		defer s.Close()
		bs = append(bs, tBackend(t, s.URL))
	}

	pickers := []struct {
		name string
		p    Picker
	}{
		{"RoundRobin", &RoundRobin{}},
		{"LeastConnections", LeastConnections{}},
		{"PowerOfTwoChoices", PowerOfTwoChoices{}},
	}
	t.Logf("%-18s %8s %8s %8s %8s %10s", "picker", "mean", "p50", "p90", "p99", "slow share")
	for _, pk := range pickers {
		for i := range hits {
			hits[i].Store(0)
		}
		pool := NewPool(Config{Picker: pk.p}, bs...)
		front := httptest.NewServer(NewHandler(pool, nil))
		var mu sync.Mutex
		var lat []time.Duration
		var wg sync.WaitGroup
		for c := 0; c < clients; c++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < perClient; i++ {
					start := time.Now()
					resp, err := tClient.Get(front.URL)
					if err != nil {
						continue
					}
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					d := time.Since(start)
					mu.Lock()
					lat = append(lat, d)
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		front.Close()
		if len(lat) == 0 {
			t.Logf("%-18s no successful requests (is NewHandler implemented?)", pk.name)
			continue
		}
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		var sum time.Duration
		for _, d := range lat {
			sum += d
		}
		q := func(f float64) time.Duration { return lat[int(f*float64(len(lat)-1))] }
		total := hits[0].Load() + hits[1].Load() + hits[2].Load() + hits[3].Load()
		share := float64(hits[slowBackend].Load()) / float64(max(total, 1))
		t.Logf("%-18s %8s %8s %8s %8s %9.1f%%", pk.name,
			(sum / time.Duration(len(lat))).Round(time.Millisecond),
			q(0.50).Round(time.Millisecond), q(0.90).Round(time.Millisecond),
			q(0.99).Round(time.Millisecond), 100*share)
	}
	t.Log("Question: which column barely changes between pickers, and why? (hint: what fraction of requests must hit the slow backend for p99 to be slow?)")
}

// TestBreakIt_DeadBackendWithAndWithoutEjection: one of 4 backends is dead.
// Count the 502s clients see over 200 requests with passive ejection
// effectively disabled (huge threshold) versus a threshold of 3.
func TestBreakIt_DeadBackendWithAndWithoutEjection(t *testing.T) {
	var bs []*Backend
	for i := 0; i < 4; i++ {
		s := tNamedServer(fmt.Sprintf("b%d", i))
		if i == 2 {
			s.Close() // dead
		} else {
			defer s.Close()
		}
		bs = append(bs, tBackend(t, s.URL))
	}
	for _, threshold := range []int{1 << 30, 3} {
		pool := NewPool(Config{Picker: &RoundRobin{}, FailureThreshold: threshold, Cooldown: time.Hour}, bs...)
		front := httptest.NewServer(NewHandler(pool, nil))
		bad := 0
		for i := 0; i < 200; i++ {
			resp, err := tClient.Get(front.URL)
			if err != nil {
				bad++
				continue
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				bad++
			}
		}
		front.Close()
		label := fmt.Sprintf("threshold=%d", threshold)
		if threshold == 1<<30 {
			label = "no ejection"
		}
		t.Logf("%-14s %3d / 200 client-visible errors", label, bad)
	}
	t.Log("Question: with no ejection, what error rate would a 1-in-20 dead backend cause, and what would a retry-on-another-backend policy change?")
}
