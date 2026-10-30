package jobq

// In-process tests for the pure pieces: the log frame format (milestone 1)
// and the retry arithmetic and classification (milestone 2).

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/http"
	"testing"
	"time"
)

func tFrames(t *testing.T, payloads ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, p := range payloads {
		if err := AppendFrame(&buf, []byte(p)); err != nil {
			t.Fatalf("AppendFrame(%q): %v", p, err)
		}
	}
	return buf.Bytes()
}

func TestM1_FramesRoundTrip(t *testing.T) {
	in := []string{`{"op":"enq","id":"j1"}`, `{"op":"ack","id":"j1"}`, "x"}
	b := tFrames(t, in...)
	if want := 3*FrameHeader + len(in[0]) + len(in[1]) + 1; len(b) != want {
		t.Fatalf("3 frames are %d bytes; want %d (8-byte header + payload each)", len(b), want)
	}
	if n := binary.BigEndian.Uint32(b[0:4]); int(n) != len(in[0]) {
		t.Fatalf("first frame length field = %d; want %d (big-endian payload length first)", n, len(in[0]))
	}
	frames, good, err := ScanFrames(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 || good != int64(len(b)) {
		t.Fatalf("ScanFrames: %d frames, good=%d; want 3 frames, good=%d", len(frames), good, len(b))
	}
	for i := range in {
		if string(frames[i]) != in[i] {
			t.Errorf("frame %d = %q; want %q", i, frames[i], in[i])
		}
	}
}

type tCountingWriter struct {
	writes int
	buf    bytes.Buffer
}

func (w *tCountingWriter) Write(p []byte) (int, error) { w.writes++; return w.buf.Write(p) }

func TestM1_AppendFrameIsASingleWrite(t *testing.T) {
	w := &tCountingWriter{}
	if err := AppendFrame(w, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if w.writes != 1 || w.buf.Len() != FrameHeader+5 {
		t.Fatalf("AppendFrame made %d Write calls (%d bytes); want 1 call of %d bytes: separate header and payload writes "+
			"can leave a header without its payload when the process dies between them", w.writes, w.buf.Len(), FrameHeader+5)
	}
	if err := AppendFrame(w, nil); err == nil {
		t.Error("AppendFrame accepted an empty payload; a zero length must stay reserved for 'not a frame'")
	}
	if err := AppendFrame(w, make([]byte, MaxFrame+1)); err == nil {
		t.Error("AppendFrame accepted a payload above MaxFrame; recovery would treat it as corruption")
	}
}

func TestM1_ScanStopsAtATornTail(t *testing.T) {
	b := tFrames(t, "first", "second")
	whole := int64(len(b))
	full := tFrames(t, "first", "second", "third-record")
	for cut := len(b) + 1; cut < len(full); cut++ { // every way the third frame can be cut short
		frames, good, err := ScanFrames(bytes.NewReader(full[:cut]))
		if err != nil {
			t.Fatalf("cut at %d: a torn tail is not an error, got %v", cut, err)
		}
		if len(frames) != 2 || good != whole {
			t.Fatalf("cut at %d: %d frames, good=%d; want the 2 complete frames and good=%d (truncate there)", cut, len(frames), good, whole)
		}
	}
}

func TestM1_ScanStopsAtACorruptFrame(t *testing.T) {
	b := tFrames(t, "first", "second", "third")
	second := FrameHeader + len("first")
	b[second+FrameHeader+2] ^= 0x40 // flip a bit inside the second payload
	frames, good, err := ScanFrames(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || good != int64(second) {
		t.Fatalf("got %d frames, good=%d; want 1 frame and good=%d: a CRC mismatch ends the good prefix, "+
			"and nothing after it can be trusted", len(frames), good, second)
	}
}

func TestM1_ScanRejectsAnAbsurdLength(t *testing.T) {
	b := tFrames(t, "ok")
	junk := make([]byte, FrameHeader+16)
	binary.BigEndian.PutUint32(junk[0:4], 0xFFFFFFF0)
	frames, good, err := ScanFrames(bytes.NewReader(append(b, junk...)))
	if err != nil || len(frames) != 1 || good != int64(len(b)) {
		t.Fatalf("got %d frames, good=%d, err=%v; want 1 frame and good=%d: a length above MaxFrame is corruption, "+
			"not a reason to allocate 4 GiB", len(frames), good, err, len(b))
	}
	zero := make([]byte, FrameHeader)
	if frames, good, _ := ScanFrames(bytes.NewReader(append(b, zero...))); len(frames) != 1 || good != int64(len(b)) {
		t.Fatalf("zero-length frame: got %d frames, good=%d; want 1 and %d (preallocated zeros are not records)", len(frames), good, len(b))
	}
}

func TestM2_BackoffWithoutJitterDoublesUpToTheCap(t *testing.T) {
	ms := time.Millisecond
	want := []time.Duration{100 * ms, 200 * ms, 400 * ms, 800 * ms, 1000 * ms, 1000 * ms}
	for i, w := range want {
		if got := Backoff(i+1, 100*ms, time.Second, NoJitter, 0.5); got != w {
			t.Errorf("Backoff(retry %d, base 100ms, max 1s, none) = %v; want %v", i+1, got, w)
		}
	}
	if got := Backoff(0, 100*ms, time.Second, NoJitter, 0); got != 100*ms {
		t.Errorf("Backoff(retry 0) = %v; want it treated as retry 1 (100ms)", got)
	}
}

func TestM2_BackoffFullJitterSpansZeroToTheCap(t *testing.T) {
	base, mx := 100*time.Millisecond, 10*time.Second
	if got := Backoff(4, base, mx, FullJitter, 0); got != 0 {
		t.Errorf("full jitter with rnd=0 = %v; want 0", got)
	}
	if got := Backoff(4, base, mx, FullJitter, 0.5); got != 400*time.Millisecond {
		t.Errorf("full jitter, retry 4 (cap 800ms), rnd=0.5 = %v; want 400ms", got)
	}
	if got := Backoff(4, base, mx, FullJitter, 0.999999); got >= 800*time.Millisecond || got < 799*time.Millisecond {
		t.Errorf("full jitter, cap 800ms, rnd→1 = %v; want just under 800ms", got)
	}
}

func TestM2_BackoffEqualJitterKeepsHalfTheCap(t *testing.T) {
	if got := Backoff(3, 100*time.Millisecond, time.Minute, EqualJitter, 0); got != 200*time.Millisecond {
		t.Errorf("equal jitter, cap 400ms, rnd=0 = %v; want 200ms (half the cap is the floor)", got)
	}
	if got := Backoff(3, 100*time.Millisecond, time.Minute, EqualJitter, 0.5); got != 300*time.Millisecond {
		t.Errorf("equal jitter, cap 400ms, rnd=0.5 = %v; want 300ms", got)
	}
}

func TestM2_BackoffNeverOverflows(t *testing.T) {
	for _, retry := range []int{63, 64, 100, 1 << 20} {
		got := Backoff(retry, 500*time.Millisecond, 30*time.Second, NoJitter, 0)
		if got != 30*time.Second {
			t.Errorf("Backoff(retry %d) = %v; want the 30s cap (2^%d × base overflows int64 if computed naively)", retry, got, retry)
		}
	}
	if got := Backoff(1, time.Minute, time.Second, NoJitter, 0); got != time.Second {
		t.Errorf("base above max: Backoff = %v; want max (1s)", got)
	}
}

func TestM2_RetryAfterIsAFloorNotACeiling(t *testing.T) {
	if got := RetryDelay(100*time.Millisecond, 2*time.Second); got != 2*time.Second {
		t.Errorf("RetryDelay(backoff 100ms, Retry-After 2s) = %v; want 2s: retrying before the vendor asked earns another 429", got)
	}
	if got := RetryDelay(3*time.Second, time.Second); got != 3*time.Second {
		t.Errorf("RetryDelay(backoff 3s, Retry-After 1s) = %v; want 3s: Retry-After is a minimum, not a target", got)
	}
}

func TestM2_ClassifyByStatusAndError(t *testing.T) {
	cases := []struct {
		status int
		err    error
		want   Outcome
		why    string
	}{
		{200, nil, Success, "2xx delivered"},
		{202, nil, Success, "2xx delivered"},
		{0, errors.New("context deadline exceeded"), Retryable, "a timeout is ambiguous: retry with the same key"},
		{0, errors.New("connection refused"), Retryable, "never arrived"},
		{http.StatusTooManyRequests, nil, Retryable, "rate limited: wait and retry"},
		{http.StatusRequestTimeout, nil, Retryable, "408 is the server timing out"},
		{http.StatusServiceUnavailable, nil, Retryable, "5xx is the vendor's problem"},
		{http.StatusInternalServerError, nil, Retryable, "5xx is the vendor's problem"},
		{http.StatusBadRequest, nil, Permanent, "the same bad request fails the same way forever"},
		{http.StatusUnauthorized, nil, Permanent, "credentials don't fix themselves"},
		{http.StatusUnprocessableEntity, nil, Permanent, "invalid payload"},
		{http.StatusMovedPermanently, nil, Permanent, "an unexpected redirect is not something retrying fixes"},
	}
	for _, c := range cases {
		if got := Classify(c.status, c.err); got != c.want {
			t.Errorf("Classify(%d, %v) = %v; want %v (%s)", c.status, c.err, got, c.want, c.why)
		}
	}
}

func TestM2_ParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	if d, ok := ParseRetryAfter("3", now); !ok || d != 3*time.Second {
		t.Errorf(`ParseRetryAfter("3") = %v, %v; want 3s, true`, d, ok)
	}
	date := now.Add(90 * time.Second).Format(http.TimeFormat)
	if d, ok := ParseRetryAfter(date, now); !ok || d != 90*time.Second {
		t.Errorf("ParseRetryAfter(%q) = %v, %v; want 1m30s, true", date, d, ok)
	}
	past := now.Add(-time.Minute).Format(http.TimeFormat)
	if d, ok := ParseRetryAfter(past, now); !ok || d != 0 {
		t.Errorf("ParseRetryAfter(past date) = %v, %v; want 0, true", d, ok)
	}
	for _, bad := range []string{"", "soon", "-5", "1.5"} {
		if _, ok := ParseRetryAfter(bad, now); ok {
			t.Errorf("ParseRetryAfter(%q) accepted a malformed value", bad)
		}
	}
}
