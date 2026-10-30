package jobq

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Jitter selects how randomness is applied to the exponential backoff cap.
// The zero value is FullJitter. Provided.
type Jitter int

const (
	FullJitter  Jitter = iota // rnd × cap: spreads retries the most
	EqualJitter               // cap/2 + rnd × cap/2: keeps a floor of half the cap
	NoJitter                  // cap exactly: synchronises clients (for comparison and tests)
)

// ParseJitter maps "full", "equal" and "none" to a Jitter. Provided.
func ParseJitter(s string) (Jitter, bool) {
	switch s {
	case "full":
		return FullJitter, true
	case "equal":
		return EqualJitter, true
	case "none":
		return NoJitter, true
	}
	return 0, false
}

// Backoff returns the delay before retry number retry (1 = the retry after the
// first failure). The cap is min(max, base × 2^(retry−1)), computed without
// overflow for any retry count; rnd in [0, 1) is applied according to j.
// retry < 1 is treated as 1.
func Backoff(retry int, base, max time.Duration, j Jitter, rnd float64) time.Duration {
	if retry < 1 {
		retry = 1
	}
	c := base
	for i := 1; i < retry && c < max; i++ {
		if c > max/2 {
			c = max
			break
		}
		c *= 2
	}
	if c > max {
		c = max
	}
	switch j {
	case NoJitter:
		return c
	case EqualJitter:
		return c/2 + time.Duration(rnd*float64(c/2))
	default:
		return time.Duration(rnd * float64(c))
	}
}

// RetryDelay is the delay a worker asks the broker for: the backoff, but never
// less than the vendor's Retry-After. The vendor's number is a floor, not a
// suggestion, and it may exceed the backoff cap.
func RetryDelay(backoff, retryAfter time.Duration) time.Duration {
	return max(backoff, retryAfter)
}

// Outcome is how a worker treats the result of one delivery attempt.
type Outcome int

const (
	Success   Outcome = iota // ack
	Retryable                // nack with a delay; the attempt may or may not have had an effect
	Permanent                // dead-letter now: retrying cannot help
)

func (o Outcome) String() string {
	switch o {
	case Success:
		return "success"
	case Retryable:
		return "retryable"
	case Permanent:
		return "permanent"
	}
	return "Outcome(" + strconv.Itoa(int(o)) + ")"
}

// Classify maps an HTTP status (0 when no response arrived) and a transport
// error to an Outcome:
//   - a transport error (timeout, refused, reset, cancelled) is Retryable: the
//     request may never have arrived, or may have been processed;
//   - 2xx is Success;
//   - 408, 425, 429 and every 5xx are Retryable;
//   - any other status (other 4xx, unexpected 1xx/3xx) is Permanent.
func Classify(status int, err error) Outcome {
	if err != nil || status == 0 {
		return Retryable
	}
	switch {
	case status >= 200 && status < 300:
		return Success
	case status == http.StatusRequestTimeout, status == http.StatusTooEarly, status == http.StatusTooManyRequests:
		return Retryable
	case status >= 500 && status < 600:
		return Retryable
	}
	return Permanent
}

// ParseRetryAfter reads a Retry-After header: delay-seconds ("120") or an
// HTTP-date. ok is false for an empty or malformed value. A date in the past is
// a zero delay.
func ParseRetryAfter(h string, now time.Time) (time.Duration, bool) {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(h, 10, 64); err == nil {
		if n < 0 {
			return 0, false
		}
		if n > int64(24*time.Hour/time.Second) {
			n = int64(24 * time.Hour / time.Second)
		}
		return time.Duration(n) * time.Second, true
	}
	t, err := http.ParseTime(h)
	if err != nil {
		return 0, false
	}
	return max(t.Sub(now), 0), true
}

// sleepCtx waits d or until ctx is done; it reports whether the full wait
// happened.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
