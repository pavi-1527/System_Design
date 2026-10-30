//go:build breakit

package lsm

// Break-it experiments for A2 Unit 2 §5 and §6.
//
//  1. The amplification triangle. The same overwrite-heavy workload is run
//     with fanouts 2..16 and with bloom filters on and off. Predict, for each
//     fanout, the write amplification, the space amplification (entries
//     stored per live key) and the number of tables a read of a missing key
//     has to binary-search, before you look at the table:
//
//     go test -tags breakit -run BreakIt_Amplification -v ./labs/a2/lsm/
//
//  2. Resurrection. Build with -tags breakit and your compaction wired to
//     breakAlwaysDropTombstones (see breakit_on.go), then run the normal
//     tombstone test and watch a deleted key come back:
//
//     go test -tags breakit -run Tombstone -v ./labs/a2/lsm/

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

func TestBreakIt_AmplificationByFanout(t *testing.T) {
	const keys, updates, memtable = 1000, 30_000, 100
	type row struct {
		fanout            int
		writeAmp, spaceAm float64
		tables            int
		searchedNoBloom   float64
		searchedBloom     float64
	}
	var rows []row
	for _, fanout := range []int{2, 3, 4, 8, 16} {
		var r row
		r.fanout = fanout
		for _, bits := range []int{0, 10} {
			db := tOpen(t, Options{MemtableEntries: memtable, Fanout: fanout, BloomBitsPerKey: bits})
			rng := rand.New(rand.NewPCG(1, 2))
			for i := 0; i < updates; i++ {
				tPut(t, db, tRandKey(rng.IntN(keys)), "balance-0000000000")
			}
			s := db.Stats()
			before := s.TableLookups
			const probes = 2000
			for i := 0; i < probes; i++ {
				db.Get(tRandKey(1_000_000 + i))
			}
			searched := float64(db.Stats().TableLookups-before) / probes
			if bits == 0 {
				r.searchedNoBloom = searched
				r.writeAmp = s.WriteAmp()
				r.spaceAm = float64(s.Entries) / keys
				r.tables = s.Tables
			} else {
				r.searchedBloom = searched
			}
		}
		rows = append(rows, r)
	}
	fmt.Printf("%7s %10s %10s %8s %22s %22s\n", "fanout", "write amp", "space amp", "tables", "searched/miss (no bf)", "searched/miss (10 bpk)")
	for _, r := range rows {
		fmt.Printf("%7d %10.2f %10.2f %8d %22.2f %22.2f\n", r.fanout, r.writeAmp, r.spaceAm, r.tables, r.searchedNoBloom, r.searchedBloom)
	}
	first, last := rows[0], rows[len(rows)-1]
	if !(first.writeAmp > last.writeAmp) {
		t.Errorf("write amplification should fall as fanout grows: fanout %d -> %.2f, fanout %d -> %.2f", first.fanout, first.writeAmp, last.fanout, last.writeAmp)
	}
	for _, r := range rows {
		if r.searchedBloom > r.searchedNoBloom/5+0.05 {
			t.Errorf("fanout %d: bloom filters cut searches per missing key only from %.2f to %.2f", r.fanout, r.searchedNoBloom, r.searchedBloom)
		}
	}
}
