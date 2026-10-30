package jobq

import (
	"context"
	"strconv"
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
//
// Milestone 2. Doubling until the cap (and stopping there) avoids both the
// overflow of base << retry and a float conversion.
func Backoff(retry int, base, max time.Duration, j Jitter, rnd float64) time.Duration {
	// TODO(lab): cap = min(max, base × 2^(retry−1)) without overflow, then apply j.
	return 0
}

// RetryDelay is the delay a worker asks the broker for: the backoff, but never
// less than the vendor's Retry-After. The vendor's number is a floor, not a
// suggestion, and it may exceed the backoff cap. Milestone 2.
func RetryDelay(backoff, retryAfter time.Duration) time.Duration {
	// TODO(lab): the larger of the two.
	return 0
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
//
// Milestone 2.
func Classify(status int, err error) Outcome {
	// TODO(lab): transport error → Retryable; 2xx → Success; 408/425/429/5xx → Retryable; else Permanent.
	return Success
}

// ParseRetryAfter reads a Retry-After header: delay-seconds ("120") or an
// HTTP-date. ok is false for an empty or malformed value. A date in the past is
// a zero delay. Very large values may be capped (the solution caps at 24 h).
// Milestone 2.
func ParseRetryAfter(h string, now time.Time) (time.Duration, bool) {
	// TODO(lab): delay-seconds or an HTTP-date (http.ParseTime); reject negatives and junk.
	return 0, false
}

// sleepCtx waits d or until ctx is done; it reports whether the full wait
// happened. Provided; handy in the worker loop.
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
