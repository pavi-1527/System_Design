package memfs

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
)

func mustUsage(t *testing.T, fs *FS, p string) Usage {
	t.Helper()
	u, err := fs.Usage(p)
	if err != nil {
		t.Fatalf("Usage(%q) = %v, want nil", p, err)
	}
	return u
}

func TestStage3_UsageCountsASubtree(t *testing.T) {
	fs := New()
	if err := fs.MkdirAll("/a/b"); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, fs, "/a/f1", "12345")
	mustWrite(t, fs, "/a/b/f2", "123")
	mustWrite(t, fs, "/g", "1")

	if got, want := mustUsage(t, fs, "/a"), (Usage{Files: 2, Dirs: 2, Bytes: 8}); got != want {
		t.Fatalf("Usage(/a) = %+v, want %+v (Dirs includes /a itself)", got, want)
	}
	if got, want := mustUsage(t, fs, "/"), (Usage{Files: 3, Dirs: 3, Bytes: 9}); got != want {
		t.Fatalf("Usage(/) = %+v, want %+v", got, want)
	}
	if got, want := mustUsage(t, fs, "/g"), (Usage{Files: 1, Bytes: 1}); got != want {
		t.Fatalf("Usage(file) = %+v, want %+v", got, want)
	}
	_, err := fs.Usage("/nope")
	wantErr(t, "Usage of a missing path", err, ErrNotExist)
}

func TestStage3_QuotaRejectsWritesThatDoNotFit(t *testing.T) {
	fs := New()
	if err := fs.SetQuota(10); err != nil {
		t.Fatalf("SetQuota = %v", err)
	}
	mustWrite(t, fs, "/a", "123456")
	wantErr(t, "write past the quota", fs.WriteFile("/b", []byte("12345")), ErrNoSpace)
	if _, err := fs.Stat("/b"); err == nil {
		t.Fatal("a write rejected with ErrNoSpace must not create the file")
	}
	mustWrite(t, fs, "/b", "1234") // exactly fills the quota
	wantErr(t, "write one byte over", fs.WriteFile("/c", []byte("x")), ErrNoSpace)
	mustWrite(t, fs, "/empty", "") // zero bytes always fit
}

func TestStage3_FailedOverwriteLeavesOldContent(t *testing.T) {
	fs := New()
	if err := fs.SetQuota(10); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, fs, "/f", "aaaaaa")
	wantErr(t, "overwrite that grows past the quota", fs.WriteFile("/f", []byte("bbbbbbbbbbbbbb")), ErrNoSpace)
	if got := mustRead(t, fs, "/f"); got != "aaaaaa" {
		t.Fatalf("file = %q after a rejected overwrite, want the old content aaaaaa (never truncate before checking)", got)
	}
	mustWrite(t, fs, "/f", "bbbbbbbbbb") // grows by 4 to exactly 10: fits
	if got := mustUsage(t, fs, "/").Bytes; got != 10 {
		t.Fatalf("Usage bytes = %d, want 10", got)
	}
}

func TestStage3_QuotaAccountingTracksShrinkRemoveRenameAndRemoveAll(t *testing.T) {
	fs := New()
	if err := fs.SetQuota(10); err != nil {
		t.Fatal(err)
	}
	full := func(what string) {
		t.Helper()
		wantErr(t, what+": a 1-byte write must not fit", fs.WriteFile("/probe", []byte("1")), ErrNoSpace)
	}
	used := func(want int64) {
		t.Helper()
		if got := mustUsage(t, fs, "/").Bytes; got != want {
			t.Fatalf("Usage bytes = %d, want %d", got, want)
		}
	}

	mustWrite(t, fs, "/a", "123456") // 6
	mustWrite(t, fs, "/b", "1234")   // 10
	full("two files")
	mustWrite(t, fs, "/a", "123") // shrinking frees space: 7
	used(7)
	mustWrite(t, fs, "/c", "123") // 10
	full("after refill")

	if err := fs.Remove("/c"); err != nil { // 7
		t.Fatal(err)
	}
	used(7)
	if err := fs.Rename("/a", "/b"); err != nil { // a (3) replaces b (4): 3
		t.Fatal(err)
	}
	used(3)
	mustMkdir(t, fs, "/dir")
	mustWrite(t, fs, "/dir/x", "1234") // 7
	mustWrite(t, fs, "/dir/y", "123")  // 10
	full("directory filled")
	if err := fs.RemoveAll("/dir"); err != nil { // frees everything beneath: 3
		t.Fatal(err)
	}
	used(3)
	mustWrite(t, fs, "/z", "1234567") // 10: proves RemoveAll really freed 7 bytes
}

func TestStage3_SetQuota(t *testing.T) {
	fs := New()
	mustWrite(t, fs, "/big", "0123456789")
	wantErr(t, "SetQuota below current usage", fs.SetQuota(5), ErrNoSpace)
	mustWrite(t, fs, "/big", "0123456789012345") // the failed SetQuota must not have changed anything
	if err := fs.SetQuota(16); err != nil {
		t.Fatalf("SetQuota(16) = %v, want nil (usage is exactly 16)", err)
	}
	wantErr(t, "write past quota", fs.WriteFile("/x", []byte("1")), ErrNoSpace)
	if err := fs.SetQuota(0); err != nil {
		t.Fatalf("SetQuota(0) = %v", err)
	}
	mustWrite(t, fs, "/x", "1") // 0 means unlimited again
}

func TestStage3_ConcurrentQuotaIsNeverExceeded(t *testing.T) {
	fs := New()
	if err := fs.SetQuota(1000); err != nil {
		t.Fatal(err)
	}
	const writers = 200 // each writes its own 100-byte file: exactly 10 can fit
	var ok, full atomic.Int64
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch err := fs.WriteFile(fmt.Sprintf("/f%03d", i), make([]byte, 100)); {
			case err == nil:
				ok.Add(1)
			case isNoSpace(err):
				full.Add(1)
			default:
				t.Errorf("WriteFile = %v", err)
			}
		}()
	}
	// A reader samples the total while the writers race: it must never see more than the quota.
	stop := make(chan struct{})
	var rwg sync.WaitGroup
	rwg.Add(1)
	go func() {
		defer rwg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if u, err := fs.Usage("/"); err == nil && u.Bytes > 1000 {
				t.Errorf("observed %d bytes stored with a quota of 1000", u.Bytes)
				return
			}
		}
	}()
	wg.Wait()
	close(stop)
	rwg.Wait()
	if ok.Load() != 10 || full.Load() != writers-10 {
		t.Fatalf("%d writes succeeded and %d got ErrNoSpace, want exactly 10 and %d: the quota check and the write must be one atomic step", ok.Load(), full.Load(), writers-10)
	}
	if got := mustUsage(t, fs, "/").Bytes; got != 1000 {
		t.Fatalf("Usage bytes = %d, want 1000", got)
	}
}

func isNoSpace(err error) bool { return errors.Is(err, ErrNoSpace) }

func TestStage3_ConcurrentOverwritesOfOneFileKeepAccountingExact(t *testing.T) {
	fs := New()
	if err := fs.SetQuota(500); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(g)))
			for range 300 {
				_ = fs.WriteFile("/shared", make([]byte, r.Intn(700))) // sometimes too big
			}
		}()
	}
	wg.Wait()
	content, err := fs.ReadFile("/shared")
	if err != nil {
		t.Fatal(err)
	}
	if got := mustUsage(t, fs, "/").Bytes; got != int64(len(content)) {
		t.Fatalf("Usage says %d bytes but the file holds %d: accounting drifted under concurrent overwrites", got, len(content))
	}
	if len(content) > 500 {
		t.Fatalf("file holds %d bytes, over the quota of 500", len(content))
	}
}

func TestStage3_RenameIsAtomicToConcurrentObservers(t *testing.T) {
	fs := New()
	mustMkdir(t, fs, "/a")
	mustMkdir(t, fs, "/b")
	mustWrite(t, fs, "/a/f", "data")

	var done atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer done.Store(true)
		for i := range 2000 {
			from, to := "/a/f", "/b/f"
			if i%2 == 1 {
				from, to = to, from
			}
			if err := fs.Rename(from, to); err != nil {
				t.Errorf("Rename(%s, %s) = %v", from, to, err)
				return
			}
		}
	}()
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !done.Load() {
				copies := 0
				err := fs.Walk("/", func(p string, i Info) error {
					if !i.IsDir {
						copies++
					}
					return nil
				})
				if err != nil {
					t.Errorf("Walk = %v", err)
					return
				}
				if copies != 1 {
					t.Errorf("a snapshot of / showed %d copies of the file, want exactly 1: Rename must be atomic and Walk a consistent snapshot", copies)
					return
				}
				if u, err := fs.Usage("/"); err != nil || u.Files != 1 {
					t.Errorf("Usage(/) = %+v, %v; want exactly 1 file at every instant", u, err)
					return
				}
			}
		}()
	}
	within(t, "concurrent rename and observers", wg.Wait)
}

func TestStage3_OppositeRenamesCannotDeadlockOrCreateCycles(t *testing.T) {
	// Two directories renamed into each other at the same time must not
	// deadlock (opposite lock order) and must never both succeed (that would
	// detach both subtrees into an unreachable cycle).
	for round := range 200 {
		fs := New()
		mustMkdir(t, fs, "/p")
		mustMkdir(t, fs, "/q")
		mustWrite(t, fs, "/p/pf", "p")
		mustWrite(t, fs, "/q/qf", "q")
		var e1, e2 error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); e1 = fs.Rename("/p", "/q/p") }()
		go func() { defer wg.Done(); e2 = fs.Rename("/q", "/p/q") }()
		within(t, "opposite renames", wg.Wait)
		if e1 == nil && e2 == nil {
			t.Fatalf("round %d: both renames succeeded; /p and /q now contain each other and are unreachable from /", round)
		}
		if u := mustUsage(t, fs, "/"); u.Files != 2 {
			t.Fatalf("round %d: %d files reachable from /, want 2 (nothing may be lost)", round, u.Files)
		}
	}
}

func TestStage3_MixedOperationsKeepTheTreeConsistent(t *testing.T) {
	fs := New()
	const quota = 4000
	if err := fs.SetQuota(quota); err != nil {
		t.Fatal(err)
	}
	dirs := []string{"/d0", "/d1", "/d2", "/d0/s", "/d1/s"}
	pick := func(r *rand.Rand) string {
		d := dirs[r.Intn(len(dirs)+1)%len(dirs)]
		return fmt.Sprintf("%s/n%d", d, r.Intn(6))
	}
	for _, d := range []string{"/d0", "/d1", "/d2"} {
		mustMkdir(t, fs, d)
	}

	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(g) + 100))
			for range 600 {
				switch r.Intn(9) {
				case 0:
					_ = fs.Mkdir(pick(r))
				case 1:
					_ = fs.MkdirAll(pick(r) + "/x/y")
				case 2, 3:
					_ = fs.WriteFile(pick(r), make([]byte, r.Intn(300)))
				case 4:
					_ = fs.Remove(pick(r))
				case 5:
					_ = fs.RemoveAll(pick(r))
				case 6:
					_ = fs.Rename(pick(r), pick(r))
				case 7:
					_, _ = fs.ReadFile(pick(r))
					_, _ = fs.List(dirs[r.Intn(len(dirs))])
					_, _ = fs.Stat(pick(r))
				case 8:
					if u, err := fs.Usage("/"); err == nil && u.Bytes > quota {
						t.Errorf("observed %d bytes with quota %d", u.Bytes, quota)
					}
				}
			}
		}()
	}
	within(t, "mixed workload", wg.Wait)

	// Invariants on the final tree.
	var files int
	var bytes int64
	seen := map[string]bool{}
	err := fs.Walk("/", func(p string, i Info) error {
		if seen[p] {
			t.Errorf("path %s visited twice", p)
		}
		seen[p] = true
		if !i.IsDir {
			files++
			bytes += i.Size
			if b, err := fs.ReadFile(p); err != nil || int64(len(b)) != i.Size {
				t.Errorf("ReadFile(%s) = %d bytes, %v; Stat said %d", p, len(b), err, i.Size)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	u := mustUsage(t, fs, "/")
	if u.Files != files || u.Bytes != bytes {
		t.Fatalf("Usage(/) = %+v but walking found %d files / %d bytes: accounting drifted", u, files, bytes)
	}
	if bytes > quota {
		t.Fatalf("%d bytes stored, quota %d", bytes, quota)
	}
}
