package jobq

// Multi-process test harness for the Track E labs.
//
// The test binary doubles as the service binary: when the environment variable
// in tChildEnv is set, TestMain does not run any tests. It decodes the JSON
// argument list from that variable and calls the package's Run, exactly as
// cmd/<name>/main.go does. tStart launches such a child, waits for the
// "READY <host:port>" line Run prints once it is listening, and hands back a
// tProc the test can kill (SIGKILL: a crash), stop (SIGTERM: a deploy),
// restart on the same address, or inspect. Children are race-instrumented
// whenever the test binary is (go test -race), and a "WARNING: DATA RACE" in
// any child's stderr fails the test that started it.
//
// A child exits by itself when its stdin closes, which happens when the test
// process dies for any reason (panic, timeout, ^C), so no test ever leaves a
// server running in the background.
//
// tProxy is an in-test TCP proxy that sits between two processes and injects
// network faults: added latency, a blackhole (connections stay open, bytes
// vanish: what a partition looks like to the caller) and refused connections.
//
// Nothing here is part of the exercise; it is identical in every Track E lab.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const tChildEnv = "SD_TRACK_E_CHILD_ARGS"

func TestMain(m *testing.M) {
	if raw, ok := os.LookupEnv(tChildEnv); ok {
		os.Exit(tChildMain(raw))
	}
	os.Exit(m.Run())
}

func tChildMain(raw string) int {
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		fmt.Fprintf(os.Stderr, "harness: bad %s: %v\n", tChildEnv, err)
		return 2
	}
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		fmt.Fprintln(os.Stderr, "harness: parent test process went away; exiting")
		os.Exit(3)
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return Run(ctx, args, os.Stdout, os.Stderr)
}

// tReadyTimeout bounds how long a child may take to print READY. A race-built
// child starts in well under a second; recovery from a large log takes longer.
const tReadyTimeout = 15 * time.Second

// tLog is a goroutine-safe writer that keeps everything written to it and
// reports the address on the first "READY <addr>" line.
type tLog struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	ready chan string
	sent  bool
}

func tNewLog() *tLog { return &tLog{ready: make(chan string, 1)} }

func (l *tLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	if !l.sent {
		for _, line := range strings.Split(l.buf.String(), "\n") {
			if addr, ok := strings.CutPrefix(strings.TrimSpace(line), "READY "); ok && addr != "" {
				l.sent = true
				l.ready <- addr
				break
			}
		}
	}
	return len(p), nil
}

func (l *tLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// tProc is one running (or exited) child process.
type tProc struct {
	t    testing.TB
	name string
	args []string

	Addr string // host:port from the READY line
	URL  string // "http://" + Addr

	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *tLog
	stderr  *tLog
	exited  chan struct{}
	code    int    // valid after exited is closed; -1 when killed by a signal
	history string // stdout+stderr of earlier incarnations (before Restart)
}

// tStart launches Run(args) in a child process and waits until it is READY.
// Pass "-addr", "127.0.0.1:0" (two separate arguments) to let the child pick a
// free port; Restart reuses the port it reported.
func tStart(t testing.TB, name string, args ...string) *tProc {
	t.Helper()
	p := &tProc{t: t, name: name, args: append([]string(nil), args...)}
	p.start()
	t.Cleanup(p.cleanup)
	return p
}

func (p *tProc) start() {
	p.t.Helper()
	exe, err := os.Executable()
	if err != nil {
		p.t.Fatalf("harness: %v", err)
	}
	raw, err := json.Marshal(p.args)
	if err != nil {
		p.t.Fatalf("harness: %v", err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), tChildEnv+"="+string(raw))
	if _, set := os.LookupEnv("GORACE"); !set {
		// A race-enabled binary sleeps 1 s at exit by default (atexit_sleep_ms)
		// so that pending reports can print; children exit often in these tests.
		cmd.Env = append(cmd.Env, "GORACE=atexit_sleep_ms=0")
	}
	p.stdout, p.stderr = tNewLog(), tNewLog()
	cmd.Stdout, cmd.Stderr = p.stdout, p.stderr
	if p.stdin, err = cmd.StdinPipe(); err != nil {
		p.t.Fatalf("harness: %v", err)
	}
	if err := cmd.Start(); err != nil {
		p.t.Fatalf("harness: start %s: %v", p.name, err)
	}
	p.cmd = cmd
	exited := make(chan struct{})
	p.exited = exited
	go func() {
		_ = cmd.Wait()
		p.code = cmd.ProcessState.ExitCode()
		close(exited)
	}()
	select {
	case addr := <-p.stdout.ready:
		p.Addr, p.URL = addr, "http://"+addr
	case <-exited:
		p.t.Fatalf("%s exited with code %d before printing READY (is Run implemented?)\nargs: %q\nstderr:\n%s",
			p.name, p.code, p.args, tTail(p.stderr.String()))
	case <-time.After(tReadyTimeout):
		_ = cmd.Process.Kill()
		<-exited
		p.t.Fatalf("%s did not print \"READY <addr>\" within %v; Run must print it once it is listening\nstderr:\n%s",
			p.name, tReadyTimeout, tTail(p.stderr.String()))
	}
}

// Running reports whether the child has not exited yet.
func (p *tProc) Running() bool {
	select {
	case <-p.exited:
		return false
	default:
		return true
	}
}

// Kill sends SIGKILL (a crash: no defers, no flushing, no goodbye) and waits.
func (p *tProc) Kill() {
	p.t.Helper()
	if !p.Running() {
		return
	}
	_ = p.cmd.Process.Kill()
	select {
	case <-p.exited:
	case <-time.After(10 * time.Second):
		p.t.Fatalf("%s did not die within 10s of SIGKILL", p.name)
	}
}

// Stop sends SIGTERM (what a deploy or an orchestrator does) and waits up to
// timeout for a graceful exit. It returns the exit code.
func (p *tProc) Stop(timeout time.Duration) int {
	p.t.Helper()
	if !p.Running() {
		return p.code
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.exited:
		return p.code
	case <-time.After(timeout):
		_ = p.cmd.Process.Kill()
		<-p.exited
		p.t.Fatalf("%s still running %v after SIGTERM; graceful shutdown must finish within its drain timeout\nstderr:\n%s",
			p.name, timeout, tTail(p.stderr.String()))
		return -1
	}
}

// Wait waits up to timeout for the child to exit on its own. ok is false if it
// is still running.
func (p *tProc) Wait(timeout time.Duration) (code int, ok bool) {
	select {
	case <-p.exited:
		return p.code, true
	case <-time.After(timeout):
		return 0, false
	}
}

// Restart kills the child if it is still running and starts it again with the
// same arguments, except that the value after "-addr" becomes the address the
// previous incarnation reported, so peers can reconnect to the same place.
func (p *tProc) Restart() {
	p.t.Helper()
	p.Kill()
	_ = p.stdin.Close()
	p.history += p.stdout.String() + p.stderr.String()
	for i := 0; i+1 < len(p.args); i++ {
		if p.args[i] == "-addr" {
			p.args[i+1] = p.Addr
		}
	}
	p.start()
}

// Stderr is everything the current incarnation wrote to stderr.
func (p *tProc) Stderr() string { return p.stderr.String() }

// Stdout is everything the current incarnation wrote to stdout.
func (p *tProc) Stdout() string { return p.stdout.String() }

func (p *tProc) cleanup() {
	if p.Running() {
		_ = p.cmd.Process.Kill()
		<-p.exited
	}
	_ = p.stdin.Close()
	all := p.history + p.stdout.String() + p.stderr.String()
	if strings.Contains(all, "WARNING: DATA RACE") {
		i := strings.Index(all, "WARNING: DATA RACE")
		p.t.Errorf("%s reported a data race (the race detector runs inside child processes too):\n%s", p.name, tHead(all[i:]))
	} else if p.t.Failed() {
		p.t.Logf("--- %s output (last lines) ---\n%s", p.name, tTail(all))
	}
}

func tHead(s string) string {
	const max = 4000
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func tTail(s string) string {
	const max = 4000
	if len(s) > max {
		return "…" + s[len(s)-max:]
	}
	return s
}

// tEventually polls cond every 20ms until it returns true or timeout passes,
// then fails with the last message cond returned.
func tEventually(t testing.TB, timeout time.Duration, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		ok, msg := cond()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %v: %s", timeout, msg)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// tHTTP never follows redirects (tests look at the 3xx itself) and never waits
// forever.
var tHTTP = &http.Client{
	Timeout:       5 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// tDo sends one request and returns the status code and body. A transport
// error is returned, not fatal: failure tests expect some.
func tDo(method, url string, body string, hdr ...string) (int, string, http.Header, error) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return 0, "", nil, err
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := tHTTP.Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header, err
}

// ---------- fault-injecting TCP proxy ----------

const (
	tPass int32 = iota
	tBlackhole
	tRefuse
)

// tProxy forwards TCP connections from Addr to target and injects faults.
type tProxy struct {
	Addr   string
	URL    string
	target string
	ln     net.Listener
	mode   atomic.Int32
	delay  atomic.Int64
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	wg     sync.WaitGroup
}

func tNewProxy(t testing.TB, target string) *tProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy: %v", err)
	}
	p := &tProxy{Addr: ln.Addr().String(), URL: "http://" + ln.Addr().String(), target: target, ln: ln,
		conns: map[net.Conn]struct{}{}}
	p.wg.Add(1)
	go p.accept()
	t.Cleanup(func() {
		_ = ln.Close()
		p.closeAll()
		p.wg.Wait()
	})
	return p
}

// Delay adds d before forwarding every chunk, in both directions.
func (p *tProxy) Delay(d time.Duration) { p.delay.Store(int64(d)) }

// Partition makes the link a blackhole: connections stay open and bytes
// vanish in both directions, so callers see silence until their own timeout.
func (p *tProxy) Partition() { p.mode.Store(tBlackhole) }

// Refuse closes every connection and closes new ones immediately: what a
// crashed peer's host looks like (fast failure).
func (p *tProxy) Refuse() { p.mode.Store(tRefuse); p.closeAll() }

// Heal restores normal forwarding and drops connections that lost bytes while
// the fault was active, so callers reconnect cleanly.
func (p *tProxy) Heal() { p.mode.Store(tPass); p.delay.Store(0); p.closeAll() }

func (p *tProxy) closeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for c := range p.conns {
		_ = c.Close()
		delete(p.conns, c)
	}
}

func (p *tProxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mode.Load() == tRefuse {
		return false
	}
	p.conns[c] = struct{}{}
	return true
}

func (p *tProxy) accept() {
	defer p.wg.Done()
	for {
		in, err := p.ln.Accept()
		if err != nil {
			return
		}
		if !p.track(in) {
			_ = in.Close()
			continue
		}
		out, err := net.DialTimeout("tcp", p.target, 2*time.Second)
		if err != nil || !p.track(out) {
			_ = in.Close()
			if out != nil {
				_ = out.Close()
			}
			continue
		}
		p.wg.Add(2)
		go p.pipe(out, in)
		go p.pipe(in, out)
	}
}

func (p *tProxy) pipe(dst, src net.Conn) {
	defer p.wg.Done()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if d := time.Duration(p.delay.Load()); d > 0 {
				time.Sleep(d)
			}
			if p.mode.Load() == tPass {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
			}
		}
		if err != nil {
			break
		}
	}
	_ = dst.Close()
	_ = src.Close()
	p.mu.Lock()
	delete(p.conns, dst)
	delete(p.conns, src)
	p.mu.Unlock()
}
