//go:build breakit

package feed

// Break-it experiment for C5 Unit 2 §5: the same social graph served with
// pure fan-out on read (every author a celebrity), the hybrid, and pure
// fan-out on write (nobody a celebrity). Run it after your implementation
// passes the normal tests:
//
//	go test -tags breakit -run BreakIt -v ./labs/c5/feed/
//
// It prints timeline insertions (write amplification) and the wall time of
// the post phase and the read phase for each threshold. Predict the
// FanoutWrites column before running: posts × followers for the ordinary
// users, plus (or minus) the celebrity's share.

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

func TestBreakIt_FanoutAmplification(t *testing.T) {
	const (
		users        = 2000
		followsEach  = 20 // ordinary follows per user
		postsPerUser = 5
	)
	for _, threshold := range []int{1, 100, math.MaxInt} {
		rng := rand.New(rand.NewPCG(7, 11))
		s := New(Config{CelebrityThreshold: threshold})
		// Everyone follows user 0 (the celebrity) plus 20 random others.
		for u := 1; u < users; u++ {
			me := fmt.Sprintf("u%d", u)
			_ = s.Follow(me, "u0")
			for i := 0; i < followsEach; i++ {
				other := fmt.Sprintf("u%d", 1+rng.IntN(users-1))
				if other != me {
					_ = s.Follow(me, other)
				}
			}
		}
		start := time.Now()
		for u := 0; u < users; u++ {
			for i := 0; i < postsPerUser; i++ {
				if _, err := s.Post(context.Background(), fmt.Sprintf("u%d", u), "x", ""); err != nil {
					t.Fatal(err)
				}
			}
		}
		postTime := time.Since(start)
		start = time.Now()
		var items int
		for u := 1; u < users; u++ {
			pg, err := s.Feed(fmt.Sprintf("u%d", u), "", 20)
			if err != nil {
				t.Fatal(err)
			}
			items += len(pg.Items)
		}
		readTime := time.Since(start)
		st := s.Stats()
		name := map[int]string{1: "all-pull", 100: "hybrid", math.MaxInt: "all-push"}[threshold]
		t.Logf("%-9s threshold=%-20d posts=%-6d fanout_writes=%-8d posts_phase=%-10v reads_phase=%-10v items_served=%d",
			name, threshold, st.Posts, st.FanoutWrites, postTime.Round(time.Millisecond), readTime.Round(time.Millisecond), items)
	}
}
