//go:build breakit

package suggest

// Break-it experiments for C12 Unit 2. Excluded from normal builds by the
// build tag; run them as described in labs/c12/README.md:
//
//	go test -tags breakit -run '^$' -bench BreakIt -benchmem ./labs/c12/suggest/
//	go test -race -tags breakit -run TestBreakIt ./labs/c12/suggest/

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// breakitCorpus makes n distinct queries over a 6-letter alphabet with
// Zipf-ish counts, so short prefixes match a large share of the corpus.
func breakitCorpus(n int) map[string]int64 {
	r := rand.New(rand.NewPCG(1, 2))
	const letters = "abcdef"
	counts := make(map[string]int64, n)
	for len(counts) < n {
		var sb strings.Builder
		l := 3 + r.IntN(10)
		for i := 0; i < l; i++ {
			sb.WriteByte(letters[r.IntN(len(letters))])
		}
		counts[sb.String()] = int64(1 + r.IntN(1000))
	}
	return counts
}

// scanIndex is the "compute the top-K at query time" design: queries sorted
// lexicographically, a binary search finds the prefix range, and the top-K
// is selected by walking the whole range. Correct, compact, and the cost
// grows with the number of queries that share the prefix.
type scanIndex struct {
	queries []string
	counts  map[string]int64
}

func newScanIndex(counts map[string]int64) *scanIndex {
	s := &scanIndex{counts: counts}
	for q := range counts {
		s.queries = append(s.queries, q)
	}
	sort.Strings(s.queries)
	return s
}

func (s *scanIndex) Suggest(prefix string, k int) []Suggestion {
	lo := sort.SearchStrings(s.queries, prefix)
	var top []Suggestion
	for i := lo; i < len(s.queries) && strings.HasPrefix(s.queries[i], prefix); i++ {
		sg := Suggestion{Query: s.queries[i], Count: s.counts[s.queries[i]]}
		j := sort.Search(len(top), func(j int) bool { return Less(sg, top[j]) })
		if j >= k {
			continue
		}
		if len(top) < k {
			top = append(top, Suggestion{})
		}
		copy(top[j+1:], top[j:len(top)-1])
		top[j] = sg
	}
	return top
}

// BenchmarkBreakIt_PrecomputedVsScan compares your index with the scan
// design on 200k queries for one-, two- and three-letter prefixes. Predict
// the ratio before you run it.
func BenchmarkBreakIt_PrecomputedVsScan(b *testing.B) {
	counts := breakitCorpus(200_000)
	ix := Build(counts, Options{K: 5})
	scan := newScanIndex(counts)
	for _, p := range []string{"a", "ab", "abc"} {
		matches := len(scan.Suggest(p, len(counts)))
		b.Run(fmt.Sprintf("precomputed/prefix=%s/matches=%d", p, matches), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				ix.Suggest(p, 5)
			}
		})
		b.Run(fmt.Sprintf("scan/prefix=%s/matches=%d", p, matches), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				scan.Suggest(p, 5)
			}
		})
	}
}

// racyService is Service with the atomic pointer replaced by a plain field.
// Run TestBreakIt_PlainPointerSwapRaces with -race and read the report.
type racyService struct {
	cur *Index
}

func (s *racyService) Swap(ix *Index) { s.cur = ix }

func (s *racyService) Suggest(prefix string, limit int) []Suggestion {
	return s.cur.Suggest(prefix, limit)
}

func TestBreakIt_PlainPointerSwapRaces(t *testing.T) {
	ixA := Build(map[string]int64{"sa": 1}, Options{})
	ixB := Build(map[string]int64{"sb": 1}, Options{})
	s := &racyService{cur: ixA}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					s.Suggest("s", 1)
				}
			}
		}()
	}
	deadline := time.Now().Add(200 * time.Millisecond)
	for i := 0; time.Now().Before(deadline); i++ {
		if i%2 == 0 {
			s.Swap(ixB)
		} else {
			s.Swap(ixA)
		}
	}
	close(stop)
	wg.Wait()
	t.Log("if this run printed no DATA RACE, run it again with -race: the plain pointer write is unsynchronised with the reads")
}
