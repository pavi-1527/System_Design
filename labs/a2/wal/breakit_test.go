//go:build breakit

package wal

// Break-it experiment for A2 Unit 2 §4: what group commit buys.
//
// 200 commits on a device whose fsync takes 2 ms. Serial committers pay one
// fsync per commit. 16 concurrent committers share fsyncs. Predict the wall
// time and the number of syncs for each before you run:
//
//	go test -tags breakit -run BreakIt -v ./labs/a2/wal/

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestBreakIt_GroupCommitVersusOneFsyncPerCommit(t *testing.T) {
	const commits = 200
	run := func(workers int) (time.Duration, int) {
		f := NewSimFile()
		f.SetSyncDelay(2 * time.Millisecond)
		l, _ := tOpen(t, f)
		var wg sync.WaitGroup
		start := time.Now()
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < commits/workers; i++ {
					if _, err := l.Commit([]byte("transfer:acct-1->acct-2:$25.00")); err != nil {
						t.Error(err)
						return
					}
				}
			}()
		}
		wg.Wait()
		return time.Since(start), f.Syncs()
	}
	fmt.Printf("%-10s %12s %8s %14s\n", "committers", "wall time", "fsyncs", "commits/sec")
	var serial, grouped time.Duration
	for _, workers := range []int{1, 4, 16} {
		d, syncs := run(workers)
		fmt.Printf("%-10d %12s %8d %14.0f\n", workers, d.Round(time.Millisecond), syncs, commits/d.Seconds())
		switch workers {
		case 1:
			serial = d
		case 16:
			grouped = d
		}
	}
	if grouped*3 > serial {
		t.Errorf("16 committers took %v vs %v serial; group commit should be several times faster", grouped, serial)
	}
}
