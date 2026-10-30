// Package waitroom is a virtual waiting room: the admission controller that a
// flash sale puts in front of its inventory. 500,000 people arrive for 18,000
// seats in the first minute; the seat-hold path can serve a few hundred
// requests a second with correct locking; the waiting room lets exactly that
// many through, in a fair order, and hands each admitted user a signed pass
// that the booking API checks without a database round trip.
//
// It is the lab for C8 Unit 2 §7. Suggested order: Join and Status (the
// queue), then Tick (the token bucket), then passes (Verify), then the
// expiry and capacity rules. Run: go test -race ./labs/c8/waitroom/
//
// Contract (the tests depend on every line):
//   - Join(userID) puts a user at the back of the queue and returns a Ticket
//     with their 1-based Position. Join is idempotent: a user already waiting
//     gets their existing ticket (same position, no second slot); a user who
//     has already been admitted and whose pass is still valid gets their pass
//     back (Position 0). When the queue holds MaxQueue users, Join returns
//     ErrQueueFull and changes nothing. An empty userID is ErrInvalid.
//   - Status(userID) returns the user's current Ticket: Position counts down
//     as users ahead are admitted or leave (1 = next); EstimatedWait is
//     Position / Rate. Once admitted, Position is 0 and Pass is set. A user
//     the room does not know, or whose pass has expired, is ErrNotInQueue.
//   - Tick() admits waiting users in FIFO order using a token bucket: tokens
//     accrue at Rate per second since the previous Tick (or since New),
//     capped at Burst; each admission spends one whole token. It returns the
//     passes it issued, in admission order. Tokens are not spent when nobody
//     is waiting, and unspent tokens carry over up to Burst. Time comes from
//     the injected clock, so tests never sleep.
//   - A Pass is "<userID>|<expiry unix seconds>|<hex HMAC-SHA256 over
//     userID|expiry with Secret>". It is valid for PassTTL from admission.
//     Verify(pass) returns the userID for a well-formed, correctly signed,
//     unexpired pass; ErrBadPass for anything malformed, signed with another
//     secret or tampered with; ErrPassExpired for a genuine pass past its
//     expiry. Use hmac.Equal, never ==.
//   - A user whose pass has expired is forgotten: Join puts them at the back
//     of the queue again, with a new ticket.
//   - Leave(userID) removes a waiting user (everyone behind them moves up).
//     Leaving after admission is ErrNotInQueue.
//   - Len() is the number of waiting (not yet admitted) users. Admitted() is
//     the number of users holding an unexpired pass.
//   - All exported methods are safe for concurrent use. Each user is admitted
//     at most once per pass lifetime, and the admitted order is the join
//     order. Nothing in this package starts a goroutine: the caller drives
//     Tick from a ticker.
//
// Hints:
//   - container/list gives you an O(1) FIFO with removal from the middle;
//     keep a map from userID to *list.Element for Join/Leave/Status.
//   - One mutex around the whole room is fine here: the critical sections
//     are microseconds and the room is not the bottleneck (the inventory is).
//   - Keep fractional tokens (a float64): at Rate 100 a Tick after 5 ms has
//     earned half a token, and the next Tick must not lose it.
//   - Sign with crypto/hmac + crypto/sha256; parse with strings.Split and
//     strconv.ParseInt; compare with hmac.Equal.
package waitroom

import (
	"errors"
	"time"
)

// Errors are values: compare with errors.Is.
var (
	ErrInvalid     = errors.New("waitroom: invalid request")
	ErrQueueFull   = errors.New("waitroom: queue is full, try again later")
	ErrNotInQueue  = errors.New("waitroom: user is not in the queue")
	ErrBadPass     = errors.New("waitroom: pass is malformed or not ours")
	ErrPassExpired = errors.New("waitroom: pass has expired")
)

var errNotImplemented = errors.New("waitroom: not implemented")

// Config configures a Room. Rate, PassTTL and Secret are required; Burst
// defaults to the larger of 1 and Rate; MaxQueue <= 0 means unlimited; Now
// defaults to time.Now.
type Config struct {
	Rate     float64          // admissions per second
	Burst    int              // maximum unspent tokens (admissions in one Tick)
	PassTTL  time.Duration    // how long an admitted user may use their pass
	MaxQueue int              // waiting users beyond which Join is refused
	Secret   []byte           // HMAC key for passes
	Now      func() time.Time // injected clock
}

// Ticket is what a user sees: their place in line, or their pass.
type Ticket struct {
	UserID        string
	Position      int           // 1-based while waiting; 0 once admitted
	EstimatedWait time.Duration // Position / Rate while waiting; 0 once admitted
	Pass          string        // set once admitted
	PassExpiresAt time.Time     // set once admitted
}

// Room is a waiting room. The zero value is not usable; call New.
type Room struct {
	cfg Config
	// TODO(lab): a mutex, the FIFO of waiting users, an index from userID to
	// their queue element, the admitted users with their pass and expiry,
	// the token-bucket level and the time of the last Tick.
}

// New validates cfg and returns an empty room.
func New(cfg Config) (*Room, error) {
	// TODO(lab): validate (Rate > 0, PassTTL > 0, Secret non-empty), apply
	// defaults (Burst, Now), copy the secret, start the bucket empty.
	return &Room{cfg: cfg}, errNotImplemented
}

// Join enqueues a user, or returns their existing ticket.
func (r *Room) Join(userID string) (Ticket, error) {
	// TODO(lab)
	_ = userID
	return Ticket{}, errNotImplemented
}

// Status returns the user's current ticket.
func (r *Room) Status(userID string) (Ticket, error) {
	// TODO(lab)
	_ = userID
	return Ticket{}, errNotImplemented
}

// Leave removes a waiting user.
func (r *Room) Leave(userID string) error {
	// TODO(lab)
	_ = userID
	return errNotImplemented
}

// Tick accrues tokens and admits users in FIFO order. It returns the passes
// issued, in admission order.
func (r *Room) Tick() []string {
	// TODO(lab): expire old passes, accrue tokens (capped at Burst), admit
	// while tokens >= 1 and someone is waiting.
	return nil
}

// Verify checks a pass's signature and expiry and returns its user.
func (r *Room) Verify(pass string) (string, error) {
	// TODO(lab): split, parse the expiry, recompute the MAC, hmac.Equal,
	// then check expiry against the clock.
	_ = pass
	return "", errNotImplemented
}

// Len is the number of waiting users.
func (r *Room) Len() int {
	// TODO(lab)
	return 0
}

// Admitted is the number of users holding an unexpired pass.
func (r *Room) Admitted() int {
	// TODO(lab)
	return 0
}
