// Package outbox is a toy transactional store, an outbox relay and an
// idempotent consumer. It is the lab for A6 Unit 2.
//
// The three pieces solve one problem: a service must change its own state
// AND tell the world about it, and the two must never disagree. Writing the
// state and then publishing (a "dual write") loses or invents events when
// the process dies in between. The outbox writes the event row in the same
// atomic transaction as the state; a relay publishes it later, at least
// once; the consumer deduplicates by message ID in the same transaction as
// its effect.
//
// Suggested order: DB.Do and Tx (the DB tests), then Relay.RunOnce, then
// Consumer.Handle. Run: go test -race ./labs/a6/outbox/
//
// # DB and Tx
//
// DB is an in-memory database with a key/value table, an outbox table and a
// dedupe ("processed message IDs") table. DB.Do(name, fn) runs fn as one
// serializable transaction: transactions run one at a time, and fn sees its
// own writes.
//
//   - If fn returns an error, nothing it did is visible (rollback) and Do
//     returns that error.
//   - Otherwise Do calls the hook with PointBeforeCommit. If the hook returns
//     an error the transaction is NOT applied and Do returns that error (a
//     crash before commit). Then the changes are applied atomically and the
//     hook is called with PointAfterCommit; if it returns an error, Do
//     returns it even though the transaction IS committed (a crash after
//     commit: the caller cannot know). The hook receives the transaction's
//     name. Hooks must not call back into the DB.
//   - Tx.Enqueue(topic, key, payload) adds an outbox row and returns its
//     message ID. Sequence numbers start at 1 and have no gaps (a rolled
//     back transaction does not consume one); the ID is "msg-<seq>". Rows
//     are ordered by sequence number, i.e. commit order.
//   - Tx.Seen / Tx.MarkSeen read and write the dedupe table.
//
// # Relay
//
// Relay.RunOnce moves rows from the outbox to the Broker.
//
//  1. In one transaction (name "relay-claim") it claims up to Batch unsent
//     rows by setting ClaimedBy = relay ID and LeaseUntil = now + Lease. A
//     row is claimable when it is unsent and unclaimed, or its lease has
//     expired (now is not before LeaseUntil). To keep per-key order, a row
//     with a non-empty Key is NOT claimable while an earlier unsent row with
//     the same Key exists that this claim did not take (held by another
//     relay, or beyond the batch limit). Rows are returned in sequence order.
//  2. The hook is called with PointRelayAfterClaim (name = relay ID).
//  3. For each claimed row, in order: Broker.Publish, then the hook with
//     PointRelayAfterPublish, then one transaction (name "relay-mark") that
//     calls MarkSent(seq, relayID), then the hook with PointRelayAfterMark.
//  4. If Publish fails, the claims of that row and of the rest of the batch
//     are released (name "relay-release") and RunOnce returns the count so
//     far and the publish error.
//  5. If MarkSent reports false (the lease was lost to another relay),
//     RunOnce stops and returns ErrLeaseLost.
//  6. A hook error is a crash: RunOnce returns immediately with that error
//     and does nothing else (claims stay until their lease expires).
//
// Delivery is therefore at least once: a crash between Publish and MarkSent
// publishes the row again after the lease expires.
//
// # Consumer
//
// Consumer.Handle(msg) runs the handler and records msg.ID in the dedupe
// table in ONE transaction (name "consume"): if the ID was already seen the
// handler is not called and Handle returns (false, nil); if the handler
// returns an error nothing is recorded and the error is returned; otherwise
// it returns (true, nil). Effect and dedupe record commit together or not at
// all, which is what makes redelivery harmless.
//
// Break-it: when breakSplitDedupe is true (go test -tags breakit) Handle must
// instead run the handler in one transaction (name "consume-effect"), call
// the hook with PointConsumerBetween (name = msg.ID), and only then record
// the ID in a second transaction (name "consume-mark"): a crash in between
// applies the effect twice.
package outbox

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Hook is called at named points; a non-nil error means "the process crashed
// here". name identifies the transaction or the relay.
type Hook func(point, name string) error

// Crash points.
const (
	PointBeforeCommit      = "tx:before-commit"
	PointAfterCommit       = "tx:after-commit"
	PointRelayAfterClaim   = "relay:after-claim"
	PointRelayAfterPublish = "relay:after-publish"
	PointRelayAfterMark    = "relay:after-mark"
	PointConsumerBetween   = "consumer:between-effect-and-dedupe"
)

// ErrLeaseLost is returned by Relay.RunOnce when another relay took over a
// row this relay had claimed.
var ErrLeaseLost = errors.New("outbox: lease lost to another relay")

// Message is what the relay hands to the broker.
type Message struct {
	ID      string
	Topic   string
	Key     string
	Payload string
}

// Row is one outbox row.
type Row struct {
	Seq        int64
	Msg        Message
	Sent       bool
	ClaimedBy  string
	LeaseUntil time.Time
}

type state struct {
	kv   map[string]string
	rows []Row
	seen map[string]bool
	next int64
}

func newState() *state {
	return &state{kv: map[string]string{}, seen: map[string]bool{}}
}

func (s *state) clone() *state {
	c := &state{kv: make(map[string]string, len(s.kv)), seen: make(map[string]bool, len(s.seen)), next: s.next}
	for k, v := range s.kv {
		c.kv[k] = v
	}
	for k, v := range s.seen {
		c.seen[k] = v
	}
	c.rows = append([]Row(nil), s.rows...)
	return c
}

// DB is the in-memory transactional store.
type DB struct {
	mu   sync.Mutex
	st   *state
	hook Hook
}

// NewDB returns an empty DB. hook may be nil.
func NewDB(hook Hook) *DB { return &DB{st: newState(), hook: hook} }

func (db *DB) fire(point, name string) error {
	if db.hook == nil {
		return nil
	}
	return db.hook(point, name)
}

// Tx is the handle fn uses inside DB.Do.
type Tx struct{ st *state }

// Do runs fn as one atomic, serializable transaction. See the package doc.
func (db *DB) Do(name string, fn func(tx *Tx) error) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	work := db.st.clone()
	if err := fn(&Tx{st: work}); err != nil {
		return err
	}
	if err := db.fire(PointBeforeCommit, name); err != nil {
		return err
	}
	db.st = work
	return db.fire(PointAfterCommit, name)
}

// Get reads a key outside any transaction.
func (db *DB) Get(key string) (string, bool) {
	db.mu.Lock()
	defer db.mu.Unlock()
	v, ok := db.st.kv[key]
	return v, ok
}

// Rows returns a snapshot of the outbox, in sequence order.
func (db *DB) Rows() []Row {
	db.mu.Lock()
	defer db.mu.Unlock()
	return append([]Row(nil), db.st.rows...)
}

// Pending counts outbox rows that are not yet marked sent.
func (db *DB) Pending() int {
	db.mu.Lock()
	defer db.mu.Unlock()
	n := 0
	for _, r := range db.st.rows {
		if !r.Sent {
			n++
		}
	}
	return n
}

// Seen reports whether a message ID is in the dedupe table.
func (db *DB) Seen(id string) bool {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.st.seen[id]
}

// Get reads a key, seeing this transaction's own writes.
func (tx *Tx) Get(key string) (string, bool) {
	v, ok := tx.st.kv[key]
	return v, ok
}

// Put writes a key.
func (tx *Tx) Put(key, value string) { tx.st.kv[key] = value }

// Enqueue adds an outbox row and returns the message ID ("msg-<seq>").
func (tx *Tx) Enqueue(topic, key, payload string) string {
	tx.st.next++
	id := fmt.Sprintf("msg-%d", tx.st.next)
	tx.st.rows = append(tx.st.rows, Row{Seq: tx.st.next, Msg: Message{ID: id, Topic: topic, Key: key, Payload: payload}})
	return id
}

// Seen reports whether the message ID is in the dedupe table.
func (tx *Tx) Seen(id string) bool { return tx.st.seen[id] }

// MarkSeen records a message ID in the dedupe table.
func (tx *Tx) MarkSeen(id string) { tx.st.seen[id] = true }

// Claim leases up to max claimable rows to relayID. See the package doc.
func (tx *Tx) Claim(relayID string, now time.Time, lease time.Duration, max int) []Row {
	var out []Row
	blocked := map[string]bool{}
	for i := range tx.st.rows {
		r := &tx.st.rows[i]
		if r.Sent {
			continue
		}
		live := r.ClaimedBy != "" && now.Before(r.LeaseUntil)
		k := r.Msg.Key
		if live || len(out) >= max || (k != "" && blocked[k]) {
			if k != "" {
				blocked[k] = true
			}
			continue
		}
		r.ClaimedBy = relayID
		r.LeaseUntil = now.Add(lease)
		out = append(out, *r)
	}
	return out
}

func (tx *Tx) row(seq int64) *Row {
	for i := range tx.st.rows {
		if tx.st.rows[i].Seq == seq {
			return &tx.st.rows[i]
		}
	}
	return nil
}

// MarkSent marks a row sent if relayID still holds its claim and it is not
// already sent. It reports whether it did.
func (tx *Tx) MarkSent(seq int64, relayID string) bool {
	r := tx.row(seq)
	if r == nil || r.Sent || r.ClaimedBy != relayID {
		return false
	}
	r.Sent = true
	return true
}

// Release gives up relayID's claim on an unsent row.
func (tx *Tx) Release(seq int64, relayID string) {
	if r := tx.row(seq); r != nil && !r.Sent && r.ClaimedBy == relayID {
		r.ClaimedBy = ""
		r.LeaseUntil = time.Time{}
	}
}

// Broker is where the relay publishes. Publish may fail; the relay retries.
type Broker interface {
	Publish(msg Message) error
}

// RelayConfig configures a Relay.
type RelayConfig struct {
	ID     string // unique per relay instance
	DB     *DB
	Broker Broker
	Lease  time.Duration    // default 30s
	Batch  int              // default 10
	Now    func() time.Time // default time.Now
	Hook   Hook
}

// Relay publishes outbox rows to a Broker with at-least-once semantics.
type Relay struct{ cfg RelayConfig }

// NewRelay applies defaults and returns a Relay.
func NewRelay(cfg RelayConfig) *Relay {
	if cfg.Lease <= 0 {
		cfg.Lease = 30 * time.Second
	}
	if cfg.Batch <= 0 {
		cfg.Batch = 10
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Relay{cfg: cfg}
}

func (r *Relay) fire(point string) error {
	if r.cfg.Hook == nil {
		return nil
	}
	return r.cfg.Hook(point, r.cfg.ID)
}

// RunOnce claims a batch, publishes it and marks it sent. See the package doc.
func (r *Relay) RunOnce() (published int, err error) {
	id, db := r.cfg.ID, r.cfg.DB
	var rows []Row
	now := r.cfg.Now()
	if err := db.Do("relay-claim", func(tx *Tx) error {
		rows = tx.Claim(id, now, r.cfg.Lease, r.cfg.Batch)
		return nil
	}); err != nil {
		return 0, err
	}
	if err := r.fire(PointRelayAfterClaim); err != nil {
		return 0, err
	}
	for i, row := range rows {
		if perr := r.cfg.Broker.Publish(row.Msg); perr != nil {
			rest := rows[i:]
			_ = db.Do("relay-release", func(tx *Tx) error {
				for _, x := range rest {
					tx.Release(x.Seq, id)
				}
				return nil
			})
			return published, perr
		}
		if err := r.fire(PointRelayAfterPublish); err != nil {
			return published, err
		}
		var marked bool
		if err := db.Do("relay-mark", func(tx *Tx) error {
			marked = tx.MarkSent(row.Seq, id)
			return nil
		}); err != nil {
			return published, err
		}
		if !marked {
			return published, ErrLeaseLost
		}
		published++
		if err := r.fire(PointRelayAfterMark); err != nil {
			return published, err
		}
	}
	return published, nil
}

// Handler applies a message's effect inside the consumer's transaction.
type Handler func(tx *Tx, msg Message) error

// Consumer applies messages exactly once in effect, however often they are
// delivered.
type Consumer struct {
	db *DB
	h  Handler
}

// NewConsumer returns a Consumer that stores its dedupe table in db.
func NewConsumer(db *DB, h Handler) *Consumer { return &Consumer{db: db, h: h} }

// Handle processes one delivery. See the package doc.
func (c *Consumer) Handle(msg Message) (applied bool, err error) {
	if breakSplitDedupe {
		if c.db.Seen(msg.ID) {
			return false, nil
		}
		if err := c.db.Do("consume-effect", func(tx *Tx) error { return c.h(tx, msg) }); err != nil {
			return false, err
		}
		if err := c.db.fire(PointConsumerBetween, msg.ID); err != nil {
			return true, err
		}
		return true, c.db.Do("consume-mark", func(tx *Tx) error { tx.MarkSeen(msg.ID); return nil })
	}
	err = c.db.Do("consume", func(tx *Tx) error {
		if tx.Seen(msg.ID) {
			return nil
		}
		if err := c.h(tx, msg); err != nil {
			return err
		}
		tx.MarkSeen(msg.ID)
		applied = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}
