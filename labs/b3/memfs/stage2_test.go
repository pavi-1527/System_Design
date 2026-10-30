package memfs

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

// fakeClock returns a strictly increasing time on every call to Now, so each
// mutation gets its own ModTime.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}

func mustStat(t *testing.T, fs *FS, p string) Info {
	t.Helper()
	i, err := fs.Stat(p)
	if err != nil {
		t.Fatalf("Stat(%q) = %v, want nil", p, err)
	}
	return i
}

func TestStage2_Stat(t *testing.T) {
	clk := newFakeClock()
	fs := NewWithClock(clk.Now)
	mustMkdir(t, fs, "/d")
	mustWrite(t, fs, "/d/f.txt", "12345")

	f := mustStat(t, fs, "/d/f.txt")
	if f.Name != "f.txt" || f.IsDir || f.Size != 5 {
		t.Fatalf("Stat(file) = %+v, want Name=f.txt IsDir=false Size=5", f)
	}
	d := mustStat(t, fs, "/d")
	if d.Name != "d" || !d.IsDir || d.Size != 0 {
		t.Fatalf("Stat(dir) = %+v, want Name=d IsDir=true Size=0", d)
	}
	r := mustStat(t, fs, "/")
	if r.Name != "/" || !r.IsDir {
		t.Fatalf(`Stat("/") = %+v, want Name="/" IsDir=true`, r)
	}
	_, err := fs.Stat("/nope")
	wantErr(t, "Stat of a missing path", err, ErrNotExist)
	_, err = fs.Stat("x")
	wantErr(t, "Stat of an invalid path", err, ErrInvalidPath)
}

func TestStage2_ModTimes(t *testing.T) {
	clk := newFakeClock()
	fs := NewWithClock(clk.Now)
	mustMkdir(t, fs, "/d")
	mustWrite(t, fs, "/d/f", "a")
	f1 := mustStat(t, fs, "/d/f").ModTime
	d1 := mustStat(t, fs, "/d").ModTime
	if !d1.Equal(f1) {
		t.Fatalf("creating /d/f should set both the file's and /d's ModTime to the same instant; file=%v dir=%v", f1, d1)
	}

	mustWrite(t, fs, "/d/f", "bb") // overwrite: file changes, directory's entry set does not
	f2 := mustStat(t, fs, "/d/f").ModTime
	d2 := mustStat(t, fs, "/d").ModTime
	if !f2.After(f1) {
		t.Fatalf("overwrite did not advance the file's ModTime (%v -> %v)", f1, f2)
	}
	if !d2.Equal(d1) {
		t.Fatalf("overwriting an existing file changed the directory's ModTime (%v -> %v); only entry changes should", d1, d2)
	}

	if err := fs.Remove("/d/f"); err != nil {
		t.Fatal(err)
	}
	if d3 := mustStat(t, fs, "/d").ModTime; !d3.After(d2) {
		t.Fatalf("removing an entry did not advance the directory's ModTime (%v -> %v)", d2, d3)
	}
}

func TestStage2_Remove(t *testing.T) {
	fs := New()
	mustMkdir(t, fs, "/d")
	mustWrite(t, fs, "/d/f", "x")
	wantErr(t, "Remove of non-empty dir", fs.Remove("/d"), ErrNotEmpty)
	if err := fs.Remove("/d/f"); err != nil {
		t.Fatalf("Remove(file) = %v", err)
	}
	if err := fs.Remove("/d"); err != nil {
		t.Fatalf("Remove(empty dir) = %v", err)
	}
	wantList(t, fs, "/")
	wantErr(t, "Remove of missing path", fs.Remove("/d"), ErrNotExist)
	wantErr(t, `Remove("/")`, fs.Remove("/"), ErrInvalidPath)
	wantErr(t, "Remove of invalid path", fs.Remove("rel"), ErrInvalidPath)
}

func TestStage2_RemoveAll(t *testing.T) {
	fs := New()
	if err := fs.MkdirAll("/a/b/c"); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, fs, "/a/b/c/f", "x")
	mustWrite(t, fs, "/a/g", "y")
	if err := fs.RemoveAll("/a/b"); err != nil {
		t.Fatalf("RemoveAll = %v", err)
	}
	wantList(t, fs, "/a", "g")
	_, err := fs.Stat("/a/b/c/f")
	wantErr(t, "Stat inside removed subtree", err, ErrNotExist)
	if err := fs.RemoveAll("/a/b"); err != nil {
		t.Fatalf("RemoveAll of a missing path = %v, want nil", err)
	}
	if err := fs.RemoveAll("/a/g"); err != nil {
		t.Fatalf("RemoveAll(file) = %v", err)
	}
	wantErr(t, `RemoveAll("/")`, fs.RemoveAll("/"), ErrInvalidPath)
}

func TestStage2_RenameFileAndDirectory(t *testing.T) {
	clk := newFakeClock()
	fs := NewWithClock(clk.Now)
	if err := fs.MkdirAll("/src/sub"); err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, fs, "/dst")
	mustWrite(t, fs, "/src/sub/f", "payload")
	mustWrite(t, fs, "/src/top", "t")
	before := mustStat(t, fs, "/src/top")

	// file, same directory
	if err := fs.Rename("/src/top", "/src/renamed"); err != nil {
		t.Fatalf("Rename(file, same dir) = %v", err)
	}
	if got := mustRead(t, fs, "/src/renamed"); got != "t" {
		t.Fatalf("renamed file content = %q", got)
	}
	if after := mustStat(t, fs, "/src/renamed"); after.Name != "renamed" || !after.ModTime.Equal(before.ModTime) {
		t.Fatalf("renamed file Stat = %+v; want Name=renamed and the ModTime unchanged (%v)", after, before.ModTime)
	}
	_, err := fs.Stat("/src/top")
	wantErr(t, "Stat of the old name", err, ErrNotExist)

	// directory with a subtree, across directories
	if err := fs.Rename("/src/sub", "/dst/moved"); err != nil {
		t.Fatalf("Rename(dir) = %v", err)
	}
	if got := mustRead(t, fs, "/dst/moved/f"); got != "payload" {
		t.Fatalf("file under moved dir = %q, want payload: the whole subtree must move", got)
	}
	_, err = fs.ReadFile("/src/sub/f")
	wantErr(t, "ReadFile at the old location", err, ErrNotExist)
	wantList(t, fs, "/src", "renamed")
	wantList(t, fs, "/dst", "moved")
	if got := mustStat(t, fs, "/dst/moved").Name; got != "moved" {
		t.Fatalf("moved dir Name = %q, want moved", got)
	}
}

func TestStage2_RenameOverwriteRules(t *testing.T) {
	fs := New()
	mustWrite(t, fs, "/f1", "one")
	mustWrite(t, fs, "/f2", "two")
	mustMkdir(t, fs, "/empty")
	mustMkdir(t, fs, "/full")
	mustWrite(t, fs, "/full/x", "x")
	mustMkdir(t, fs, "/d2")

	wantErr(t, "file onto directory", fs.Rename("/f1", "/empty"), ErrIsDir)
	wantErr(t, "directory onto file", fs.Rename("/empty", "/f1"), ErrNotDir)
	wantErr(t, "directory onto non-empty directory", fs.Rename("/d2", "/full"), ErrNotEmpty)

	if err := fs.Rename("/f1", "/f2"); err != nil {
		t.Fatalf("Rename(file onto file) = %v, want nil (replace)", err)
	}
	if got := mustRead(t, fs, "/f2"); got != "one" {
		t.Fatalf("after replace /f2 = %q, want one", got)
	}
	_, err := fs.Stat("/f1")
	wantErr(t, "Stat of the renamed source", err, ErrNotExist)

	if err := fs.Rename("/d2", "/empty"); err != nil {
		t.Fatalf("Rename(dir onto empty dir) = %v, want nil (replace)", err)
	}
	wantList(t, fs, "/", "empty", "f2", "full")
}

func TestStage2_RenameInvalid(t *testing.T) {
	fs := New()
	if err := fs.MkdirAll("/a/b"); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, fs, "/a/f", "x")

	wantErr(t, "dir into its own subtree", fs.Rename("/a", "/a/b/c"), ErrInvalidMove)
	wantErr(t, "dir into its direct child", fs.Rename("/a", "/a/z"), ErrInvalidMove)
	wantErr(t, "missing source", fs.Rename("/nope", "/x"), ErrNotExist)
	wantErr(t, "missing destination parent", fs.Rename("/a/f", "/zzz/f"), ErrNotExist)
	wantErr(t, "destination parent is a file", fs.Rename("/a/b", "/a/f/b"), ErrNotDir)
	wantErr(t, "root as source", fs.Rename("/", "/x"), ErrInvalidPath)
	wantErr(t, "root as destination", fs.Rename("/a", "/"), ErrInvalidPath)
	wantErr(t, "relative source", fs.Rename("a", "/x"), ErrInvalidPath)
	wantErr(t, "relative destination", fs.Rename("/a", "x"), ErrInvalidPath)
	if err := fs.Rename("/a/f", "/a//./f"); err != nil {
		t.Fatalf("Rename to the same (cleaned) path = %v, want nil no-op", err)
	}
	if got := mustRead(t, fs, "/a/f"); got != "x" {
		t.Fatalf("file lost by a no-op rename: %q", got)
	}
	wantList(t, fs, "/a", "b", "f") // failed renames changed nothing
}

func TestStage2_RenameSiblingWithSharedStringPrefix(t *testing.T) {
	// "/ab" starts with the string "/a" but is not inside the directory /a.
	// A string-prefix "is it inside itself?" check wrongly rejects this.
	fs := New()
	mustMkdir(t, fs, "/a")
	mustWrite(t, fs, "/a/f", "x")
	if err := fs.Rename("/a", "/ab"); err != nil {
		t.Fatalf("Rename(/a, /ab) = %v, want nil (compare path components, not string prefixes)", err)
	}
	if got := mustRead(t, fs, "/ab/f"); got != "x" {
		t.Fatalf("content under /ab = %q", got)
	}
}

func TestStage2_WalkOrderAndPaths(t *testing.T) {
	fs := New()
	for _, d := range []string{"/b", "/a", "/a/z", "/a/y"} {
		mustMkdir(t, fs, d)
	}
	mustWrite(t, fs, "/a/y/f", "12")
	mustWrite(t, fs, "/c", "")
	var paths []string
	var sizes []int64
	err := fs.Walk("/", func(p string, i Info) error {
		paths = append(paths, p)
		sizes = append(sizes, i.Size)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk = %v", err)
	}
	want := []string{"/", "/a", "/a/y", "/a/y/f", "/a/z", "/b", "/c"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("Walk visited %q, want %q (parents before children, siblings sorted)", paths, want)
	}
	if sizes[3] != 2 {
		t.Fatalf("Info.Size for /a/y/f = %d, want 2", sizes[3])
	}

	paths = nil
	if err := fs.Walk("/a/", func(p string, _ Info) error { paths = append(paths, p); return nil }); err != nil {
		t.Fatal(err)
	}
	if want := []string{"/a", "/a/y", "/a/y/f", "/a/z"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("Walk(/a/) visited %q, want %q (paths are cleaned and absolute)", paths, want)
	}

	paths = nil
	if err := fs.Walk("/c", func(p string, _ Info) error { paths = append(paths, p); return nil }); err != nil || !reflect.DeepEqual(paths, []string{"/c"}) {
		t.Fatalf("Walk on a file visited %q (err %v), want just /c", paths, err)
	}
	wantErr(t, "Walk of missing root", fs.Walk("/nope", func(string, Info) error { return nil }), ErrNotExist)
}

func TestStage2_WalkSkipDir(t *testing.T) {
	fs := New()
	if err := fs.MkdirAll("/a/skipme/deep"); err != nil {
		t.Fatal(err)
	}
	if err := fs.MkdirAll("/a/skipme-not"); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, fs, "/a/skipme/deep/f", "x")
	mustWrite(t, fs, "/a/skipme-not/f", "x")
	var paths []string
	err := fs.Walk("/", func(p string, _ Info) error {
		paths = append(paths, p)
		if p == "/a/skipme" {
			return ErrSkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk = %v, want nil (ErrSkipDir is not an error)", err)
	}
	want := []string{"/", "/a", "/a/skipme", "/a/skipme-not", "/a/skipme-not/f"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("Walk visited %q, want %q (skip the subtree, not siblings that share a name prefix)", paths, want)
	}
}

func TestStage2_WalkStopsOnError(t *testing.T) {
	fs := New()
	for _, p := range []string{"/a", "/b", "/c"} {
		mustMkdir(t, fs, p)
	}
	boom := errors.New("boom")
	var seen []string
	err := fs.Walk("/", func(p string, _ Info) error {
		seen = append(seen, p)
		if p == "/b" {
			return boom
		}
		return nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Walk = %v, want the callback's error", err)
	}
	if want := []string{"/", "/a", "/b"}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("Walk visited %q after the error, want it to stop at %q", seen, want)
	}
}

func TestStage2_WalkCallbackMayCallBackIntoTheFS(t *testing.T) {
	fs := New()
	mustMkdir(t, fs, "/d")
	mustWrite(t, fs, "/d/a", "1")
	mustWrite(t, fs, "/d/b", "2")
	within(t, "Walk whose callback writes to the file system", func() {
		err := fs.Walk("/d", func(p string, i Info) error {
			if i.IsDir {
				return nil
			}
			// Needs the write lock: deadlocks if Walk still holds the read lock.
			return fs.WriteFile(p+".bak", []byte("backup of "+p))
		})
		if err != nil {
			t.Errorf("Walk = %v", err)
		}
	})
	wantList(t, fs, "/d", "a", "a.bak", "b", "b.bak")
}
