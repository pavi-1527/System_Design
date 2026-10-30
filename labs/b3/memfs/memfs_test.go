package memfs

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// within fails the test if fn does not return in time. It is how these tests
// turn a deadlock in your implementation into a message instead of a hang.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatalf("%s did not finish within %v (deadlock: a lock held while calling back into the FS?)", what, testTimeout)
	}
}

func mustMkdir(t *testing.T, fs *FS, p string) {
	t.Helper()
	if err := fs.Mkdir(p); err != nil {
		t.Fatalf("Mkdir(%q) = %v, want nil", p, err)
	}
}

func mustWrite(t *testing.T, fs *FS, p, content string) {
	t.Helper()
	if err := fs.WriteFile(p, []byte(content)); err != nil {
		t.Fatalf("WriteFile(%q) = %v, want nil", p, err)
	}
}

func mustRead(t *testing.T, fs *FS, p string) string {
	t.Helper()
	b, err := fs.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile(%q) = %v, want nil", p, err)
	}
	return string(b)
}

func wantErr(t *testing.T, what string, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("%s = %v, want an error satisfying errors.Is(err, %v)", what, got, want)
	}
}

func wantList(t *testing.T, fs *FS, p string, want ...string) {
	t.Helper()
	got, err := fs.List(p)
	if err != nil {
		t.Fatalf("List(%q) = %v, want nil", p, err)
	}
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List(%q) = %q, want %q (names only, sorted)", p, got, want)
	}
}

// ---------------------------------------------------------------- Stage 1

func TestStage1_WriteThenReadRoundTrip(t *testing.T) {
	fs := New()
	mustWrite(t, fs, "/hello.txt", "hi")
	if got := mustRead(t, fs, "/hello.txt"); got != "hi" {
		t.Fatalf("ReadFile = %q, want %q", got, "hi")
	}
	mustWrite(t, fs, "/hello.txt", "replaced")
	if got := mustRead(t, fs, "/hello.txt"); got != "replaced" {
		t.Fatalf("after overwrite ReadFile = %q, want %q (WriteFile truncates)", got, "replaced")
	}
	mustWrite(t, fs, "/hello.txt", "x")
	if got := mustRead(t, fs, "/hello.txt"); got != "x" {
		t.Fatalf("after shorter overwrite ReadFile = %q, want %q (stale tail bytes?)", got, "x")
	}
	mustWrite(t, fs, "/empty", "")
	if got := mustRead(t, fs, "/empty"); got != "" {
		t.Fatalf("empty file read back as %q", got)
	}
}

func TestStage1_MkdirAndNesting(t *testing.T) {
	fs := New()
	mustMkdir(t, fs, "/a")
	mustMkdir(t, fs, "/a/b")
	mustWrite(t, fs, "/a/b/c.txt", "deep")
	if got := mustRead(t, fs, "/a/b/c.txt"); got != "deep" {
		t.Fatalf("ReadFile = %q, want deep", got)
	}
	wantErr(t, "Mkdir of existing dir", fs.Mkdir("/a"), ErrExist)
	wantErr(t, `Mkdir("/")`, fs.Mkdir("/"), ErrExist)
	wantErr(t, "Mkdir with missing parent", fs.Mkdir("/x/y"), ErrNotExist)
	wantErr(t, "Mkdir under a file", fs.Mkdir("/a/b/c.txt/d"), ErrNotDir)
	wantErr(t, "Mkdir over a file", fs.Mkdir("/a/b/c.txt"), ErrExist)
}

func TestStage1_MkdirAll(t *testing.T) {
	fs := New()
	if err := fs.MkdirAll("/a/b/c"); err != nil {
		t.Fatalf("MkdirAll = %v, want nil", err)
	}
	wantList(t, fs, "/a", "b")
	wantList(t, fs, "/a/b", "c")
	if err := fs.MkdirAll("/a/b/c"); err != nil {
		t.Fatalf("MkdirAll on existing dir = %v, want nil (idempotent)", err)
	}
	if err := fs.MkdirAll("/"); err != nil {
		t.Fatalf(`MkdirAll("/") = %v, want nil`, err)
	}
	mustWrite(t, fs, "/a/file", "f")
	wantErr(t, "MkdirAll through a file", fs.MkdirAll("/a/file/x/y"), ErrNotDir)
	wantErr(t, "MkdirAll onto a file", fs.MkdirAll("/a/file"), ErrNotDir)
	wantList(t, fs, "/a", "b", "file") // nothing partially created
}

func TestStage1_ErrorsForWriteAndRead(t *testing.T) {
	fs := New()
	mustMkdir(t, fs, "/d")
	mustWrite(t, fs, "/f", "x")
	wantErr(t, "WriteFile onto a directory", fs.WriteFile("/d", []byte("x")), ErrIsDir)
	wantErr(t, `WriteFile("/")`, fs.WriteFile("/", []byte("x")), ErrIsDir)
	wantErr(t, "WriteFile with missing parent", fs.WriteFile("/nope/f", nil), ErrNotExist)
	wantErr(t, "WriteFile under a file", fs.WriteFile("/f/g", nil), ErrNotDir)
	_, err := fs.ReadFile("/d")
	wantErr(t, "ReadFile of a directory", err, ErrIsDir)
	_, err = fs.ReadFile("/missing")
	wantErr(t, "ReadFile of a missing file", err, ErrNotExist)
	_, err = fs.ReadFile("/f/g")
	wantErr(t, "ReadFile through a file", err, ErrNotDir)
	// A failed write must not have clobbered anything.
	if got := mustRead(t, fs, "/f"); got != "x" {
		t.Fatalf("file changed by failed writes: %q", got)
	}
}

func TestStage1_PathsAreCleaned(t *testing.T) {
	fs := New()
	mustMkdir(t, fs, "/a")
	mustMkdir(t, fs, "/a/b")
	mustWrite(t, fs, "/a//b/./../b/c.txt", "ok") // = /a/b/c.txt
	for _, p := range []string{"/a/b/c.txt", "//a/b//c.txt", "/a/b/../b/c.txt", "/../a/b/c.txt", "/a/b/c.txt/"} {
		if got := mustRead(t, fs, p); got != "ok" {
			t.Fatalf("ReadFile(%q) = %q, want ok: paths must be cleaned lexically", p, got)
		}
	}
	wantList(t, fs, "/a/b/", "c.txt")
	wantList(t, fs, "/..", "a")
	wantList(t, fs, "/a/..", "a")
	wantList(t, fs, "///", "a")
}

func TestStage1_InvalidPathsRejectedEverywhere(t *testing.T) {
	fs := New()
	for _, p := range []string{"", "relative", "a/b", "./a", "a\x00b", "/a\x00"} {
		wantErr(t, "Mkdir("+p+")", fs.Mkdir(p), ErrInvalidPath)
		wantErr(t, "MkdirAll("+p+")", fs.MkdirAll(p), ErrInvalidPath)
		wantErr(t, "WriteFile("+p+")", fs.WriteFile(p, nil), ErrInvalidPath)
		_, err := fs.ReadFile(p)
		wantErr(t, "ReadFile("+p+")", err, ErrInvalidPath)
		_, err = fs.List(p)
		wantErr(t, "List("+p+")", err, ErrInvalidPath)
	}
}

func TestStage1_ListIsSortedNamesOnly(t *testing.T) {
	fs := New()
	wantList(t, fs, "/") // empty root
	for _, n := range []string{"b", "a", "Z", "c.txt", "10", "9"} {
		mustWrite(t, fs, "/"+n, n)
	}
	mustMkdir(t, fs, "/dir")
	wantList(t, fs, "/", "10", "9", "Z", "a", "b", "c.txt", "dir") // byte order
	wantList(t, fs, "/b", "b")                                     // a file lists as itself
	_, err := fs.List("/nope")
	wantErr(t, "List of a missing path", err, ErrNotExist)
	_, err = fs.List("/b/x")
	wantErr(t, "List through a file", err, ErrNotDir)
	mustMkdir(t, fs, "/dir/sub")
	wantList(t, fs, "/dir", "sub")
}

func TestStage1_DataIsCopiedBothWays(t *testing.T) {
	fs := New()
	in := []byte("original")
	if err := fs.WriteFile("/f", in); err != nil {
		t.Fatal(err)
	}
	in[0] = 'X' // caller mutates its own slice afterwards
	if got := mustRead(t, fs, "/f"); got != "original" {
		t.Fatalf("file content changed to %q after the caller mutated its slice: WriteFile must copy", got)
	}
	out, _ := fs.ReadFile("/f")
	out[0] = 'Y' // caller mutates the returned slice
	if got := mustRead(t, fs, "/f"); got != "original" {
		t.Fatalf("file content changed to %q after the caller mutated ReadFile's result: ReadFile must copy", got)
	}
	a, _ := fs.ReadFile("/f")
	b, _ := fs.ReadFile("/f")
	if len(a) > 0 && &a[0] == &b[0] {
		t.Fatal("two ReadFile calls returned the same backing array")
	}
	if !bytes.Equal(a, b) {
		t.Fatal("reads disagree")
	}
}
