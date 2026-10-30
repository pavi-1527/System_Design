package rkv

// Helpers for the E2 suite, on top of harness_test.go. Not part of the
// exercise.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Pause freezes the child with SIGSTOP: it keeps its sockets and its memory
// but runs no code, like a leader stuck in a long GC pause or a VM that was
// suspended. Resume (SIGCONT) lets it carry on as if nothing had happened.
func (p *tProc) Pause() {
	p.t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		p.t.Fatalf("SIGSTOP %s: %v", p.name, err)
	}
}

// Resume undoes Pause.
func (p *tProc) Resume() {
	p.t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGCONT); err != nil {
		p.t.Fatalf("SIGCONT %s: %v", p.name, err)
	}
}

// tNodeArgs builds the arguments for a node; extra flags come last and win.
func tNodeArgs(id, dir string, extra ...string) []string {
	return append([]string{"node", "-id", id, "-addr", "127.0.0.1:0", "-data", dir, "-fetch-wait", "300ms"}, extra...)
}

// tStartLeader starts a node with the leader role. The process name is the id.
func tStartLeader(t *testing.T, id string, extra ...string) *tProc {
	t.Helper()
	return tStart(t, id, tNodeArgs(id, filepath.Join(t.TempDir(), id), extra...)...)
}

// tStartFollower starts a node with the follower role, fetching from leader.
func tStartFollower(t *testing.T, id, leader string, extra ...string) *tProc {
	t.Helper()
	args := append([]string{"-role", "follower", "-leader", leader}, extra...)
	return tStart(t, id, tNodeArgs(id, filepath.Join(t.TempDir(), id), args...)...)
}

// tStartCoord starts a coordinator for nodes (their process names are their ids).
func tStartCoord(t *testing.T, nodes ...*tProc) *tProc {
	t.Helper()
	var refs []string
	for _, n := range nodes {
		refs = append(refs, n.name+"="+n.URL)
	}
	return tStart(t, "coord", "coord", "-addr", "127.0.0.1:0", "-nodes", strings.Join(refs, ","),
		"-interval", "100ms", "-fail-after", "3", "-lease", "1s", "-check-timeout", "250ms")
}

func tDataDir(p *tProc) string {
	for i := 0; i+1 < len(p.args); i++ {
		if p.args[i] == "-data" {
			return p.args[i+1]
		}
	}
	return ""
}

func tGetJSON(url string, v any) error {
	resp, err := tHTTP.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func tStatus(t *testing.T, p *tProc) NodeStatus {
	t.Helper()
	var st NodeStatus
	if err := tGetJSON(p.URL+"/status", &st); err != nil {
		t.Fatalf("%s /status: %v", p.name, err)
	}
	return st
}

func tCoordStatus(t *testing.T, c *tProc) CoordStatus {
	t.Helper()
	var st CoordStatus
	if err := tGetJSON(c.URL+"/status", &st); err != nil {
		t.Fatalf("coord /status: %v", err)
	}
	return st
}

// tPut writes key=val and decodes a 200 body. err is a transport error.
func tPut(p *tProc, key, val string) (code int, res WriteResult, body string, err error) {
	code, body, _, err = tDo("PUT", p.URL+"/kv/"+key, val)
	if err == nil && code == http.StatusOK {
		if jerr := json.Unmarshal([]byte(body), &res); jerr != nil {
			err = fmt.Errorf("200 with a body that is not a WriteResult: %q", body)
		}
	}
	return code, res, body, err
}

func tMustPut(t *testing.T, p *tProc, key, val string) WriteResult {
	t.Helper()
	code, res, body, err := tPut(p, key, val)
	if err != nil || code != http.StatusOK {
		t.Fatalf("PUT %s=%s on %s: %d %s %v", key, val, p.name, code, body, err)
	}
	if res.Index == 0 {
		t.Fatalf("PUT %s on %s returned index 0; every write needs an index (it is the read-your-writes token)", key, p.name)
	}
	return res
}

// tEventuallyPut retries a PUT until it is acknowledged (a new leader may need
// its first lease or its first follower).
func tEventuallyPut(t *testing.T, p *tProc, key, val string, timeout time.Duration) WriteResult {
	t.Helper()
	var res WriteResult
	tEventually(t, timeout, func() (bool, string) {
		code, r, body, err := tPut(p, key, val)
		res = r
		return err == nil && code == http.StatusOK, fmt.Sprintf("PUT %s on %s: %d %s %v", key, p.name, code, body, err)
	})
	return res
}

func tGet(p *tProc, key, query string) (int, string, http.Header, error) {
	return tDo("GET", p.URL+"/kv/"+key+query, "")
}

// tWaitIndex waits until p has applied index.
func tWaitIndex(t *testing.T, p *tProc, index uint64, timeout time.Duration) {
	t.Helper()
	tEventually(t, timeout, func() (bool, string) {
		var st NodeStatus
		if err := tGetJSON(p.URL+"/status", &st); err != nil {
			return false, err.Error()
		}
		return st.LastIndex >= index, fmt.Sprintf("%s last_index %d, want ≥ %d", p.name, st.LastIndex, index)
	})
}

// tExpectValue fails unless key on p reads as want.
func tExpectValue(t *testing.T, p *tProc, key, want string) {
	t.Helper()
	code, body, _, err := tGet(p, key, "")
	if err != nil || code != http.StatusOK || body != want {
		t.Fatalf("GET %s on %s = %d %q %v; want 200 %q", key, p.name, code, body, err, want)
	}
}

func tFollower(st NodeStatus, id string) (FollowerStatus, bool) {
	for _, f := range st.Followers {
		if f.ID == id {
			return f, true
		}
	}
	return FollowerStatus{}, false
}

func tAppendFile(t *testing.T, path string, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open %s: %v (does the node keep its WAL at <data>/%s?)", path, err, WALFileName)
	}
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
}
