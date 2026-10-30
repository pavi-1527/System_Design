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
package waitroom

import (
	"container/list"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
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

type waiter struct {
	userID string
	elem   *list.Element
}

type admitted struct {
	pass      string
	expiresAt time.Time
}

// Room is a waiting room. The zero value is not usable; call New.
type Room struct {
	cfg      Config
	mu       sync.Mutex
	queue    *list.List // of string userID, front = next to admit
	waiting  map[string]*list.Element
	admitted map[string]admitted
	tokens   float64
	lastTick time.Time
}

// New validates cfg and returns an empty room.
func New(cfg Config) (*Room, error) {
	if cfg.Rate <= 0 || math.IsNaN(cfg.Rate) || math.IsInf(cfg.Rate, 0) {
		return nil, fmt.Errorf("%w: Rate must be positive", ErrInvalid)
	}
	if cfg.PassTTL <= 0 {
		return nil, fmt.Errorf("%w: PassTTL must be positive", ErrInvalid)
	}
	if len(cfg.Secret) == 0 {
		return nil, fmt.Errorf("%w: Secret is required", ErrInvalid)
	}
	if cfg.Burst <= 0 {
		cfg.Burst = int(math.Max(1, math.Ceil(cfg.Rate)))
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.Secret = append([]byte(nil), cfg.Secret...)
	return &Room{
		cfg:      cfg,
		queue:    list.New(),
		waiting:  map[string]*list.Element{},
		admitted: map[string]admitted{},
		lastTick: cfg.Now(),
	}, nil
}

// expireLocked forgets admitted users whose pass is no longer valid.
func (r *Room) expireLocked(now time.Time) {
	for id, a := range r.admitted {
		if !now.Before(a.expiresAt) {
			delete(r.admitted, id)
		}
	}
}

func (r *Room) positionLocked(e *list.Element) int {
	pos := 1
	for cur := r.queue.Front(); cur != nil && cur != e; cur = cur.Next() {
		pos++
	}
	return pos
}

func (r *Room) ticketLocked(userID string, now time.Time) (Ticket, error) {
	if a, ok := r.admitted[userID]; ok {
		return Ticket{UserID: userID, Pass: a.pass, PassExpiresAt: a.expiresAt}, nil
	}
	e, ok := r.waiting[userID]
	if !ok {
		return Ticket{}, fmt.Errorf("%w: %s", ErrNotInQueue, userID)
	}
	pos := r.positionLocked(e)
	return Ticket{
		UserID:        userID,
		Position:      pos,
		EstimatedWait: time.Duration(float64(pos) / r.cfg.Rate * float64(time.Second)),
	}, nil
}

// Join enqueues a user, or returns their existing ticket.
func (r *Room) Join(userID string) (Ticket, error) {
	if userID == "" {
		return Ticket{}, fmt.Errorf("%w: empty user id", ErrInvalid)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.cfg.Now()
	r.expireLocked(now)
	if _, ok := r.admitted[userID]; ok {
		return r.ticketLocked(userID, now)
	}
	if _, ok := r.waiting[userID]; ok {
		return r.ticketLocked(userID, now)
	}
	if r.cfg.MaxQueue > 0 && r.queue.Len() >= r.cfg.MaxQueue {
		return Ticket{}, fmt.Errorf("%w: %d waiting", ErrQueueFull, r.queue.Len())
	}
	r.waiting[userID] = r.queue.PushBack(userID)
	return r.ticketLocked(userID, now)
}

// Status returns the user's current ticket.
func (r *Room) Status(userID string) (Ticket, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.cfg.Now()
	r.expireLocked(now)
	return r.ticketLocked(userID, now)
}

// Leave removes a waiting user.
func (r *Room) Leave(userID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.waiting[userID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotInQueue, userID)
	}
	r.queue.Remove(e)
	delete(r.waiting, userID)
	return nil
}

// Tick accrues tokens and admits users in FIFO order. It returns the passes
// issued, in admission order.
func (r *Room) Tick() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.cfg.Now()
	r.expireLocked(now)
	if elapsed := now.Sub(r.lastTick); elapsed > 0 {
		r.tokens = math.Min(float64(r.cfg.Burst), r.tokens+elapsed.Seconds()*r.cfg.Rate)
	}
	r.lastTick = now
	var passes []string
	for r.tokens >= 1 && r.queue.Len() > 0 {
		e := r.queue.Front()
		userID := e.Value.(string)
		r.queue.Remove(e)
		delete(r.waiting, userID)
		exp := now.Add(r.cfg.PassTTL)
		pass := r.sign(userID, exp)
		r.admitted[userID] = admitted{pass: pass, expiresAt: exp}
		passes = append(passes, pass)
		r.tokens--
	}
	return passes
}

func (r *Room) mac(userID string, expUnix int64) string {
	h := hmac.New(sha256.New, r.cfg.Secret)
	h.Write([]byte(userID))
	h.Write([]byte{'|'})
	h.Write([]byte(strconv.FormatInt(expUnix, 10)))
	return hex.EncodeToString(h.Sum(nil))
}

func (r *Room) sign(userID string, exp time.Time) string {
	return userID + "|" + strconv.FormatInt(exp.Unix(), 10) + "|" + r.mac(userID, exp.Unix())
}

// Verify checks a pass's signature and expiry and returns its user.
func (r *Room) Verify(pass string) (string, error) {
	parts := strings.Split(pass, "|")
	if len(parts) != 3 || parts[0] == "" {
		return "", ErrBadPass
	}
	expUnix, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", ErrBadPass
	}
	want, err := hex.DecodeString(r.mac(parts[0], expUnix))
	if err != nil {
		return "", ErrBadPass
	}
	got, err := hex.DecodeString(parts[2])
	if err != nil || !hmac.Equal(got, want) {
		return "", ErrBadPass
	}
	if !r.cfg.Now().Before(time.Unix(expUnix, 0)) {
		return "", ErrPassExpired
	}
	return parts[0], nil
}

// Len is the number of waiting users.
func (r *Room) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.queue.Len()
}

// Admitted is the number of users holding an unexpired pass.
func (r *Room) Admitted() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expireLocked(r.cfg.Now())
	return len(r.admitted)
}
