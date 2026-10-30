//go:build breakit

package btree

// Break-it experiment for A2 Unit 1 §2: why random primary keys hurt a B-tree.
//
// The tree is told to report every page it touches (SetTouchHook). A tiny
// LRU "buffer pool" of 100 pages sits behind it. Inserting 50,000 keys in
// ascending order keeps touching the same rightmost path, so nearly every
// page access is a pool hit. Inserting the same number of keys in random
// order touches a random leaf out of ~1,100 each time, and the pool can hold
// fewer than one in ten of them.
//
// Predict misses per insert for ascending, descending and random order
// before you run:
//
//	go test -tags breakit -run BreakIt -v ./labs/a2/btree/

import (
	"container/list"
	"fmt"
	"math/rand/v2"
	"testing"
)

type tPool struct {
	cap    int
	ll     *list.List
	where  map[int]*list.Element
	hits   int
	misses int
}

func tNewPool(capacity int) *tPool {
	return &tPool{cap: capacity, ll: list.New(), where: map[int]*list.Element{}}
}

func (p *tPool) touch(id int) {
	if e, ok := p.where[id]; ok {
		p.hits++
		p.ll.MoveToFront(e)
		return
	}
	p.misses++
	p.where[id] = p.ll.PushFront(id)
	if p.ll.Len() > p.cap {
		old := p.ll.Back()
		p.ll.Remove(old)
		delete(p.where, old.Value.(int))
	}
}

func TestBreakIt_RandomKeysThrashTheBufferPool(t *testing.T) {
	const n, maxKeys, poolPages = 50_000, 64, 100
	orders := []struct {
		name string
		keys func() []int
	}{
		{"ascending", func() []int { return tSeq(n, false) }},
		{"descending", func() []int { return tSeq(n, true) }},
		{"random", func() []int {
			s := tSeq(n, false)
			r := rand.New(rand.NewPCG(11, 12))
			r.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] })
			return s
		}},
	}
	missesPerInsert := map[string]float64{}
	fmt.Printf("%-11s %8s %8s %8s %12s %10s\n", "order", "height", "leaves", "fill", "misses/insert", "hit rate")
	for _, o := range orders {
		tr := tNew(t, maxKeys)
		pool := tNewPool(poolPages)
		tr.SetTouchHook(pool.touch)
		for _, i := range o.keys() {
			tPut(t, tr, tKey(i), "v")
		}
		st := tr.Stats()
		mpi := float64(pool.misses) / n
		missesPerInsert[o.name] = mpi
		fmt.Printf("%-11s %8d %8d %7.0f%% %12.3f %9.1f%%\n", o.name, st.Height, st.Leaves, st.LeafFill*100,
			mpi, 100*float64(pool.hits)/float64(pool.hits+pool.misses))
	}
	if missesPerInsert["random"] < 10*missesPerInsert["ascending"] {
		t.Errorf("random inserts missed %.3f pages per insert vs %.3f ascending; expected at least 10x more",
			missesPerInsert["random"], missesPerInsert["ascending"])
	}
}
