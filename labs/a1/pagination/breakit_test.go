//go:build breakit

package pagination

// Break-it experiment for A1 Unit 2 §2: walk the same live table with offset
// paging and keyset paging while it is being mutated between pages, and count
// the anomalies a member would see.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a1/pagination/

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"
)

type tMutator struct {
	s    *Store
	r    *rand.Rand
	next int
}

// mutate applies a few random changes: new postings at the top, backdated
// late postings in the middle, and reversals (deletes) anywhere.
func (m *tMutator) mutate(deletable func() []string) {
	for k := 0; k < 3; k++ {
		switch m.r.IntN(3) {
		case 0:
			m.next++
			_ = m.s.Insert(Item{ID: fmt.Sprintf("new-%04d", m.next), CreatedAt: tT0.Add(time.Hour + time.Duration(m.next)*time.Second)})
		case 1:
			m.next++
			_ = m.s.Insert(Item{ID: fmt.Sprintf("late-%04d", m.next), CreatedAt: tT0.Add(time.Duration(m.r.IntN(500)) * time.Second)})
		case 2:
			ids := deletable()
			if len(ids) > 0 {
				m.s.Delete(ids[m.r.IntN(len(ids))])
			}
		}
	}
}

func TestBreakIt_OffsetVersusKeysetUnderLiveWrites(t *testing.T) {
	const n, limit = 500, 20
	type result struct {
		dups, skips int
	}
	run := func(mode string, seed uint64) result {
		s := NewStore()
		for i := 0; i < n; i++ {
			_ = s.Insert(Item{ID: fmt.Sprintf("tx-%04d", i), CreatedAt: tT0.Add(time.Duration(i) * time.Second)})
		}
		// Only "volatile" rows (inserted during the walk) are ever deleted, so
		// every original row exists for the whole walk and must be seen once.
		var volatile []string
		m := &tMutator{s: s, r: rand.New(rand.NewPCG(seed, 1))}
		del := func() []string {
			volatile = volatile[:0]
			for _, it := range s.Offset(0, s.Len()) {
				if len(it.ID) > 3 && it.ID[:3] != "tx-" {
					volatile = append(volatile, it.ID)
				}
			}
			return volatile
		}
		seen := map[string]int{}
		switch mode {
		case "offset":
			for off := 0; off < 10*n; off += limit {
				page := s.Offset(off, limit)
				if len(page) == 0 {
					break
				}
				for _, it := range page {
					seen[it.ID]++
				}
				m.mutate(del)
			}
		case "keyset":
			p := NewPaginator(s, tSecret)
			cursor := ""
			for i := 0; i < 10*n; i++ {
				pg, err := p.List(cursor, limit)
				if err != nil {
					t.Fatalf("List: %v", err)
				}
				for _, it := range pg.Items {
					seen[it.ID]++
				}
				if pg.NextCursor == "" {
					break
				}
				cursor = pg.NextCursor
				m.mutate(del)
			}
		}
		var r result
		for i := 0; i < n; i++ {
			switch c := seen[fmt.Sprintf("tx-%04d", i)]; {
			case c == 0:
				r.skips++
			case c > 1:
				r.dups += c - 1
			}
		}
		return r
	}
	t.Logf("%-7s %-6s %10s %10s", "mode", "seed", "duplicates", "skipped")
	for seed := uint64(1); seed <= 3; seed++ {
		for _, mode := range []string{"offset", "keyset"} {
			r := run(mode, seed)
			t.Logf("%-7s %-6d %10d %10d", mode, seed, r.dups, r.skips)
			if mode == "keyset" && (r.dups != 0 || r.skips != 0) {
				t.Errorf("keyset mode showed anomalies (dups=%d skips=%d); your After/List has a bug", r.dups, r.skips)
			}
		}
	}
	t.Log("Question: the deletes here only remove rows inserted during the walk. Why does offset mode still skip original rows?")
}
