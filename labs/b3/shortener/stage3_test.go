package shortener

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStage3_ParallelShortenOfOneURLYieldsOneCode(t *testing.T) {
	st := newStore()
	st.yield = true // widen the window between "is it known?" and "store it"
	s := newSvc(t, Config{Store: st})
	urls := []string{"https://example.com/hot", "HTTPS://EXAMPLE.com:443/hot", " https://example.com/hot "}
	const n = 64
	codes := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, err := s.Shorten(bg, urls[i%len(urls)])
			if err != nil {
				t.Errorf("Shorten = %v", err)
			}
			codes[i] = code
		}()
	}
	within(t, "parallel Shorten", wg.Wait)
	for _, c := range codes {
		if c != codes[0] {
			t.Fatalf("parallel Shorten of one URL returned %q and %q: check-then-act on the dedupe index", codes[0], c)
		}
	}
	if st.Len() != 1 {
		t.Fatalf("store holds %d records for one URL, want 1 (the rest are orphaned codes)", st.Len())
	}
}

func TestStage3_ParallelShortenOfDistinctURLs(t *testing.T) {
	st := newStore()
	s := newSvc(t, Config{Store: st})
	const urls, dup = 200, 3
	codes := make([][dup]string, urls)
	var wg sync.WaitGroup
	for i := range urls {
		for d := range dup {
			wg.Add(1)
			go func() {
				defer wg.Done()
				code, err := s.Shorten(bg, fmt.Sprintf("https://example.com/page/%d", i))
				if err != nil {
					t.Errorf("Shorten = %v", err)
				}
				codes[i][d] = code
			}()
		}
	}
	within(t, "parallel Shorten", wg.Wait)
	seen := map[string]int{}
	for i := range urls {
		for d := 1; d < dup; d++ {
			if codes[i][d] != codes[i][0] {
				t.Fatalf("URL %d got codes %q and %q", i, codes[i][0], codes[i][d])
			}
		}
		if j, dupe := seen[codes[i][0]]; dupe {
			t.Fatalf("URLs %d and %d share code %q", j, i, codes[i][0])
		}
		seen[codes[i][0]] = i
		mustResolve(t, s, codes[i][0], fmt.Sprintf("https://example.com/page/%d", i))
	}
	if st.Len() != urls {
		t.Fatalf("store holds %d records, want %d", st.Len(), urls)
	}
}

func TestStage3_ParallelAliasClaimsHaveExactlyOneWinner(t *testing.T) {
	st := newStore()
	st.yield = true
	s := newSvc(t, Config{Store: st})
	const n = 32
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = alias(t, s, fmt.Sprintf("https://example.com/claimant/%d", i), "launch-day", 0)
		}()
	}
	within(t, "parallel claims", wg.Wait)
	winner := -1
	for i, err := range errs {
		switch {
		case err == nil:
			if winner >= 0 {
				t.Fatalf("claimants %d and %d both got the alias", winner, i)
			}
			winner = i
		case !errors.Is(err, ErrAliasTaken):
			t.Fatalf("claimant %d got %v, want nil or ErrAliasTaken", i, err)
		}
	}
	if winner < 0 {
		t.Fatal("no claimant got the alias")
	}
	mustResolve(t, s, "launch-day", fmt.Sprintf("https://example.com/claimant/%d", winner))
}

func TestStage3_ParallelClaimsOfAnExpiredAliasHaveOneWinnerWhoseRecordSurvives(t *testing.T) {
	for round := range 20 {
		clk := newClock()
		st := newStore()
		st.yield = true
		s := newSvc(t, Config{Store: st, Now: clk.Now})
		if _, err := alias(t, s, "https://example.com/old", "summer", time.Hour); err != nil {
			t.Fatal(err)
		}
		clk.Advance(2 * time.Hour)
		const n = 16
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = alias(t, s, fmt.Sprintf("https://example.com/new/%d", i), "summer", 0)
			}()
		}
		within(t, "claims of an expired alias", wg.Wait)
		winner := -1
		for i, err := range errs {
			if err == nil {
				if winner >= 0 {
					t.Fatalf("round %d: claimants %d and %d both reclaimed the alias", round, winner, i)
				}
				winner = i
			} else if !errors.Is(err, ErrAliasTaken) {
				t.Fatalf("round %d: claimant %d got %v", round, i, err)
			}
		}
		if winner < 0 {
			t.Fatalf("round %d: nobody reclaimed an expired alias", round)
		}
		// A loser that "reclaims" after the winner already did must not delete the winner's record.
		mustResolve(t, s, "summer", fmt.Sprintf("https://example.com/new/%d", winner))
	}
}

func TestStage3_ClickCountsAreExactUnderConcurrency(t *testing.T) {
	s := newSvc(t, Config{Store: NewShardedStore(8)})
	code := mustShorten(t, s, "https://example.com/a")
	const g, per = 100, 50
	var wg sync.WaitGroup
	for range g {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range per {
				if _, err := s.Resolve(bg, code); err != nil {
					t.Errorf("Resolve = %v", err)
					return
				}
				_, _ = s.Stats(bg, code)
			}
		}()
	}
	within(t, "concurrent Resolve", wg.Wait)
	st, err := s.Stats(bg, code)
	if err != nil || st.Clicks != g*per {
		t.Fatalf("Clicks = %d (%v), want %d: lost updates on the counter", st.Clicks, err, g*per)
	}
}

func TestStage3_ResolveRacingDelete(t *testing.T) {
	s := newSvc(t, Config{Store: newStore()})
	code := mustShorten(t, s, "https://example.com/a")
	var deleted atomic.Bool
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				after := deleted.Load() // sampled BEFORE the call
				_, err := s.Resolve(bg, code)
				switch {
				case err == nil:
					if after {
						t.Error("Resolve succeeded after Delete had returned: a deleted link is still served")
						return
					}
				case errors.Is(err, ErrNotFound):
					if after {
						return
					}
				default:
					t.Errorf("Resolve = %v, want nil or ErrNotFound", err)
					return
				}
			}
		}()
	}
	time.Sleep(time.Millisecond) // let the resolvers get going; correctness does not depend on it
	if err := s.Delete(bg, code); err != nil {
		t.Fatal(err)
	}
	deleted.Store(true)
	within(t, "resolvers noticing the delete", wg.Wait)
	if _, err := s.Stats(bg, code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stats after Delete = %v, want ErrNotFound", err)
	}
}

func TestStage3_StoreFailureMidwayLeavesNoHalfCreatedState(t *testing.T) {
	const u = "https://example.com/a"
	t.Run("PutFailsThenRetrySucceeds", func(t *testing.T) {
		st := newStore()
		st.failPut = func(call int, _ Record) error {
			if call == 1 {
				return errBoom
			}
			return nil
		}
		s := newSvc(t, Config{Store: st, IDs: ids(1, 2, 3), Encoder: enc})
		_, err := s.Shorten(bg, u)
		wantErr(t, "first Shorten", err, errBoom)
		code := mustShorten(t, s, u)
		mustResolve(t, s, code, u) // a dangling index entry would hand out a code that resolves to nothing
		if st.Len() != 1 {
			t.Fatalf("store holds %d records, want 1", st.Len())
		}
	})
	t.Run("AliasPutFailsThenTheAliasIsStillFree", func(t *testing.T) {
		st := newStore()
		st.failPut = func(call int, _ Record) error {
			if call == 1 {
				return errBoom
			}
			return nil
		}
		s := newSvc(t, Config{Store: st})
		_, err := alias(t, s, u, "launch", 0)
		wantErr(t, "failing claim", err, errBoom)
		if _, err := alias(t, s, u, "launch", 0); err != nil {
			t.Fatalf("claim after a failed claim = %v: the failure must not burn the alias", err)
		}
	})
	t.Run("CannotVerifyDedupeHitDoesNotMintADuplicate", func(t *testing.T) {
		st := newStore()
		s := newSvc(t, Config{Store: st, IDs: ids(1, 2, 3), Encoder: enc})
		code := mustShorten(t, s, u)
		st.failGet = func(string) error { return errBoom }
		_, err := s.Shorten(bg, u)
		wantErr(t, "Shorten while the store cannot be read", err, errBoom)
		st.failGet = nil
		if st.Len() != 1 {
			t.Fatalf("store holds %d records, want 1: an unverifiable hit must be an error, not a second code", st.Len())
		}
		if again := mustShorten(t, s, u); again != code {
			t.Fatalf("Shorten = %q after recovery, want the original %q", again, code)
		}
	})
	t.Run("DeleteFailureKeepsTheLinkAlive", func(t *testing.T) {
		st := newStore()
		s := newSvc(t, Config{Store: &deleteFails{st}, IDs: ids(1), Encoder: enc})
		code := mustShorten(t, s, u)
		mustResolve(t, s, code, u)
		wantErr(t, "Delete while the store fails", s.Delete(bg, code), errBoom)
		mustResolve(t, s, code, u)
		if got, _ := s.Stats(bg, code); got.Clicks != 2 {
			t.Fatalf("Clicks = %d, want 2: a failed Delete must not reset the counter", got.Clicks)
		}
	})
}

// deleteFails decorates a Store whose Delete always fails: a one-line proof
// that the Service depends on the interface and nothing else.
type deleteFails struct{ Store }

func (deleteFails) Delete(_ context.Context, _ string) error { return errBoom }

func TestStage3_ShardedStorePassesTheContract(t *testing.T) {
	RunStoreContract(t, func() Store { return NewShardedStore(8) })
	RunStoreContract(t, func() Store { return NewShardedStore(1) })
}

func TestStage3_ShardedStoreSpreadsCodes(t *testing.T) {
	st := NewShardedStore(8)
	if len(st.ShardLens()) != 8 {
		t.Fatalf("ShardLens has %d entries, want 8", len(st.ShardLens()))
	}
	for i := range 1000 {
		if err := st.Put(bg, Record{Code: fmt.Sprintf("code-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	total := 0
	for i, n := range st.ShardLens() {
		total += n
		if n == 0 || n > 250 {
			t.Errorf("shard %d holds %d of 1000 codes: the hash is not spreading them (want roughly 125 each)", i, n)
		}
	}
	if total != 1000 {
		t.Fatalf("shards hold %d records in total, want 1000", total)
	}
	if n := len(NewShardedStore(0).ShardLens()); n != 16 {
		t.Fatalf("NewShardedStore(0) has %d shards, want the default 16", n)
	}
}

func TestStage3_AnyStoreWorksUnderAMixedConcurrentWorkload(t *testing.T) {
	stores := map[string]func() Store{
		"MemoryStore":  func() Store { return NewMemoryStore() },
		"ShardedStore": func() Store { return NewShardedStore(8) },
		"fakeStore":    func() Store { f := newStore(); f.yield = true; return f },
	}
	for name, mk := range stores {
		t.Run(name, func(t *testing.T) {
			clk := newClock()
			s := newSvc(t, Config{Store: mk(), Now: clk.Now})
			urls := make([]string, 12)
			for i := range urls {
				urls[i] = fmt.Sprintf("https://example.com/item/%d", i)
			}
			known := map[string]bool{}
			for _, u := range urls {
				known[u] = true
			}
			var wg sync.WaitGroup
			for g := range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := range 300 {
						u := urls[(g*7+i)%len(urls)]
						switch i % 6 {
						case 0, 1:
							code, err := s.Shorten(bg, u)
							if err != nil {
								t.Errorf("Shorten = %v", err)
								return
							}
							if got, err := s.Resolve(bg, code); err == nil && !known[got] {
								t.Errorf("Resolve(%q) = %q: not a URL we ever shortened", code, got)
								return
							}
						case 2:
							_, err := s.ShortenWith(bg, u, ShortenOptions{Alias: fmt.Sprintf("al-%d", i%5), TTL: time.Hour})
							if err != nil && !errors.Is(err, ErrAliasTaken) {
								t.Errorf("claim = %v", err)
								return
							}
						case 3:
							_ = s.Delete(bg, fmt.Sprintf("al-%d", i%5))
						case 4:
							_, _ = s.Stats(bg, fmt.Sprintf("al-%d", i%5))
						case 5:
							clk.Advance(time.Minute)
						}
					}
				}()
			}
			within(t, "mixed workload", wg.Wait)
			for _, u := range urls {
				code, err := s.Shorten(bg, u)
				if err != nil {
					t.Fatalf("Shorten(%q) = %v", u, err)
				}
				mustResolve(t, s, code, u)
			}
		})
	}
}
