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
	"time"
)

var errNotImplemented = errors.New("outbox: not implemented")

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

// DB is the in-memory transactional store.
type DB struct {
	// TODO(lab): a mutex and the committed state: the key/value map, the
	// outbox rows, the dedupe set and the last sequence number. A simple way
	// to get atomicity: run fn against a copy of the state and swap the copy
	// in only if everything succeeds.
	hook Hook
}

// NewDB returns an empty DB. hook may be nil.
func NewDB(hook Hook) *DB {
	// TODO(lab)
	return &DB{hook: hook}
}

// Tx is the handle fn uses inside DB.Do.
type Tx struct {
	// TODO(lab): whatever your Do needs to give fn a private working view.
}

// Do runs fn as one atomic, serializable transaction. See the package doc for
// the rollback and crash-hook rules.
func (db *DB) Do(name string, fn func(tx *Tx) error) error {
	// TODO(lab)
	return errNotImplemented
}

// Get reads a key outside any transaction.
func (db *DB) Get(key string) (string, bool) {
	// TODO(lab)
	return "", false
}

// Rows returns a snapshot (a copy) of the outbox, in sequence order.
func (db *DB) Rows() []Row {
	// TODO(lab)
	return nil
}

// Pending counts outbox rows that are not yet marked sent.
func (db *DB) Pending() int {
	// TODO(lab)
	return 0
}

// Seen reports whether a message ID is in the dedupe table.
func (db *DB) Seen(id string) bool {
	// TODO(lab)
	return false
}

// Get reads a key, seeing this transaction's own writes.
func (tx *Tx) Get(key string) (string, bool) {
	// TODO(lab)
	return "", false
}

// Put writes a key.
func (tx *Tx) Put(key, value string) {
	// TODO(lab)
}

// Enqueue adds an outbox row and returns the message ID ("msg-<seq>").
func (tx *Tx) Enqueue(topic, key, payload string) string {
	// TODO(lab)
	return ""
}

// Seen reports whether the message ID is in the dedupe table.
func (tx *Tx) Seen(id string) bool {
	// TODO(lab)
	return false
}

// MarkSeen records a message ID in the dedupe table.
func (tx *Tx) MarkSeen(id string) {
	// TODO(lab)
}

// Claim leases up to max claimable rows to relayID until now+lease and
// returns them in sequence order. See "Relay" in the package doc for which
// rows are claimable (expired leases, per-key ordering).
func (tx *Tx) Claim(relayID string, now time.Time, lease time.Duration, max int) []Row {
	// TODO(lab)
	return nil
}

// MarkSent marks a row sent if relayID still holds its claim and the row is
// not already sent. It reports whether it did.
func (tx *Tx) MarkSent(seq int64, relayID string) bool {
	// TODO(lab)
	return false
}

// Release gives up relayID's claim on an unsent row.
func (tx *Tx) Release(seq int64, relayID string) {
	// TODO(lab)
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

// NewRelay applies the defaults and returns a Relay.
func NewRelay(cfg RelayConfig) *Relay {
	// TODO(lab)
	return &Relay{cfg: cfg}
}

// RunOnce claims a batch, publishes it and marks it sent. See the package doc
// for the exact sequence, the hook points and the error cases.
func (r *Relay) RunOnce() (published int, err error) {
	// TODO(lab)
	return 0, errNotImplemented
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
func NewConsumer(db *DB, h Handler) *Consumer {
	// TODO(lab)
	return &Consumer{db: db, h: h}
}

// Handle processes one delivery: handler + dedupe record in ONE transaction.
// Returns (false, nil) for a duplicate, (true, nil) when applied.
//
// Break-it: when breakSplitDedupe is true (go test -tags breakit), do it the
// wrong way instead, as described in the package doc, and watch the tests
// and the break-it table change. Call c.db.hook (if non-nil) for the
// PointConsumerBetween point.
func (c *Consumer) Handle(msg Message) (applied bool, err error) {
	// TODO(lab)
	_ = breakSplitDedupe
	return false, errNotImplemented
}
