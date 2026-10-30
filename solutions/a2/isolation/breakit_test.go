//go:build breakit

package isolation

// Break-it experiments for A2 Unit 3 §7-§8. Predict the numbers before you
// run them:
//
//	go test -tags breakit -run BreakIt -v ./labs/a2/isolation/
//
//  1. Lost updates. 8 workers each increment one counter 200 times with a
//     read, a pause, and a write. What is the final value at each level, and
//     how many attempts did each level need?
//  2. Write skew under load. Checking and savings hold $100 each and the rule
//     is checking+savings >= 0. 8 workers withdraw random amounts from a random
//     account after checking the rule. What is the lowest combined balance
//     ever observed at each level?
//
// Then, in your copy of Commit, delete the write-conflict check and re-run the
// normal suite to see which tests notice, and which anomaly each one is about.

import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"strconv"
	"sync"
	"testing"
)

func TestBreakIt_LostUpdatesByIsolationLevel(t *testing.T) {
	const workers, each = 8, 200
	fmt.Printf("%-20s %10s %10s %10s\n", "level", "expected", "final", "attempts")
	var rcFinal int
	for _, lvl := range tAllLevels {
		s := NewStore()
		tSeed(t, s, map[string]string{"counter": "0"})
		var wg sync.WaitGroup
		var mu sync.Mutex
		attempts := 0
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < each; i++ {
					n, err := s.Run(lvl, 100_000, func(tx *Tx) error {
						v, _ := tx.Get("counter")
						c, _ := strconv.Atoi(v)
						runtime.Gosched() // widen the read-to-write window
						return tx.Put("counter", strconv.Itoa(c+1))
					})
					if err != nil {
						panic(err)
					}
					mu.Lock()
					attempts += n
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		final := tCommittedInt(t, s, "counter")
		fmt.Printf("%-20v %10d %10d %10d\n", lvl, workers*each, final, attempts)
		if lvl == ReadCommitted {
			rcFinal = final
		} else if final != workers*each {
			t.Errorf("%v lost updates: final %d", lvl, final)
		}
	}
	if rcFinal >= workers*each {
		t.Errorf("read committed lost no updates in %d increments; the anomaly should be easy to hit", workers*each)
	}
}

func TestBreakIt_WriteSkewUnderLoad(t *testing.T) {
	fmt.Printf("%-20s %22s %10s\n", "level", "lowest combined balance", "attempts")
	for _, lvl := range tAllLevels {
		s := NewStore()
		tSeed(t, s, map[string]string{"checking": "100", "savings": "100"})
		var wg sync.WaitGroup
		var mu sync.Mutex
		lowest, attempts := 200, 0
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				r := rand.New(rand.NewPCG(uint64(w), 3))
				for i := 0; i < 200; i++ {
					acct := []string{"checking", "savings"}[r.IntN(2)]
					amt := 1 + r.IntN(60)
					n, err := s.Run(lvl, 100_000, func(tx *Tx) error {
						c, _ := tx.Get("checking")
						v, _ := tx.Get("savings")
						ci, _ := strconv.Atoi(c)
						vi, _ := strconv.Atoi(v)
						runtime.Gosched()
						if ci+vi-amt < 0 {
							return nil
						}
						cur := ci
						if acct == "savings" {
							cur = vi
						}
						return tx.Put(acct, strconv.Itoa(cur-amt))
					})
					if err != nil {
						panic(err)
					}
					mu.Lock()
					attempts += n
					if sum := tCommittedInt(t, s, "checking") + tCommittedInt(t, s, "savings"); sum < lowest {
						lowest = sum
					}
					mu.Unlock()
				}
			}(w)
		}
		wg.Wait()
		if sum := tCommittedInt(t, s, "checking") + tCommittedInt(t, s, "savings"); sum < lowest {
			lowest = sum
		}
		fmt.Printf("%-20v %22d %10d\n", lvl, lowest, attempts)
		if lvl == Serializable && lowest < 0 {
			t.Errorf("serializable allowed the combined balance to reach %d", lowest)
		}
	}
}
