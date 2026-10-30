// Package tinylog is a tiny in-memory partitioned append-only log with
// consumer groups, in the style of Kafka. It is the lab for A6 Unit 1.
//
// Suggested order: Log (Append, Read, retention), then Group and Consumer
// (Join, Poll, Commit), then the rebalance and fencing tests.
// Run: go test -race ./labs/a6/tinylog/
//
// # Log
//
// A Log has a fixed number of partitions. Each partition is an append-only
// sequence of Messages with dense, never-reused offsets starting at 0.
//
//   - Append(key, value) picks the partition: a non-empty key goes to
//     Config.Hash(key) % Partitions (default hash: FNV-1a 32-bit), so the same
//     key always lands in the same partition. An empty key is spread
//     round-robin over partitions 0, 1, 2, ... in call order.
//   - AppendTo writes to an explicit partition.
//   - Read(p, offset, max) returns up to max messages with Offset >= offset,
//     in offset order. offset == EndOffset(p) is valid and returns no messages.
//     offset < StartOffset(p) (truncated by retention) or > EndOffset(p) is
//     ErrOffsetOutOfRange. An unknown partition is ErrNoSuchPartition.
//   - Retention: when MaxMessages > 0 a partition keeps only its newest
//     MaxMessages messages; when MaxAge > 0 messages older than MaxAge (by
//     Config.Now) are dropped. Both are enforced after every append, and
//     ApplyRetention enforces them on demand (use it after advancing a fake
//     clock). Truncation moves StartOffset forward; offsets of surviving
//     messages never change.
//
// # Groups
//
// Log.Group(id) returns the consumer group with that id, creating it if
// needed. A group tracks one committed offset per partition: the offset of
// the next message to consume. A partition that was never committed reads
// from its StartOffset.
//
//   - Group.Join(memberID) adds a member (ErrDuplicateMember if the id is
//     taken) and rebalances: the generation number increases by one and the
//     partitions are reassigned. Assignment is deterministic: members sorted
//     by id, partition p goes to members[p % len(members)]. Members beyond
//     the partition count get nothing.
//   - Rebalancing is stop-the-world. Consumers that were already in the group
//     are stale until they call Rejoin: Poll returns ErrRebalance and Commit
//     returns ErrStaleGeneration. Join returns a consumer that is already in
//     the new generation.
//   - Rejoin adopts the current generation and assignment and resets the
//     consumer's read position on every assigned partition to the group's
//     committed offset. Messages that were polled but never committed are
//     therefore delivered again (at-least-once).
//   - Poll(max) returns at most max messages from the consumer's assigned
//     partitions, taking one message at a time from each partition in
//     ascending partition order, repeating until max is reached or nothing is
//     left (so one busy partition cannot starve the others). It advances the
//     consumer's in-memory position but commits nothing. If a position has
//     fallen behind StartOffset because of retention, it jumps forward to
//     StartOffset and Group.Skipped grows by the number of messages lost.
//   - Commit(m) records m.Offset+1 as the committed offset of m.Partition
//     (everything before it counts as processed). It never moves a committed
//     offset backwards. A consumer that is no longer a member (Close, Evict)
//     gets ErrFenced; one that missed a rebalance gets ErrStaleGeneration; a
//     partition not assigned to the consumer is ErrNotAssigned.
//   - Close removes the consumer from the group (rebalance) and is
//     idempotent. Evict does the same on the coordinator's behalf, as after a
//     missed heartbeat; the evicted Consumer value is a zombie and every
//     later call on it returns ErrFenced.
//   - ResetOffset sets a committed offset (an operator replaying history).
//   - Lag(p) is EndOffset(p) minus the committed offset (never counting
//     messages already deleted by retention); TotalLag sums it.
//
// Everything is safe for concurrent use.
package tinylog

import (
	"errors"
	"time"
)

var errNotImplemented = errors.New("tinylog: not implemented")

// Errors returned by the package.
var (
	ErrNoSuchPartition  = errors.New("tinylog: no such partition")
	ErrOffsetOutOfRange = errors.New("tinylog: offset out of range")
	ErrDuplicateMember  = errors.New("tinylog: member id already in group")
	ErrRebalance        = errors.New("tinylog: group rebalanced, call Rejoin")
	ErrStaleGeneration  = errors.New("tinylog: commit from a stale generation")
	ErrNotAssigned      = errors.New("tinylog: partition not assigned to this consumer")
	ErrFenced           = errors.New("tinylog: consumer is no longer a member of the group")
)

// Message is one record in a partition.
type Message struct {
	Partition int
	Offset    int64
	Key       string
	Value     string
	Time      time.Time
}

// Config configures a Log. Zero values get defaults.
type Config struct {
	Partitions  int                     // default 1
	MaxMessages int                     // retention by count per partition; 0 = unlimited
	MaxAge      time.Duration           // retention by age; 0 = unlimited
	Now         func() time.Time        // default time.Now
	Hash        func(key string) uint32 // default FNV-1a 32-bit (hash/fnv)
}

// Log is a partitioned append-only log.
type Log struct {
	cfg Config
	// TODO(lab): a mutex, one slice of Messages per partition plus the offset
	// of its first retained message, a round-robin counter for keyless
	// appends, and the map of groups.
}

// New returns an empty Log. Apply the defaults documented on Config.
func New(cfg Config) *Log {
	// TODO(lab)
	return &Log{cfg: cfg}
}

// Partitions returns the number of partitions.
func (l *Log) Partitions() int {
	// TODO(lab)
	return 0
}

// PartitionFor returns the partition a non-empty key maps to:
// Hash(key) % Partitions.
func (l *Log) PartitionFor(key string) int {
	// TODO(lab)
	return 0
}

// Append writes a message and returns where it landed. Empty key: round
// robin. Retention is enforced afterwards.
func (l *Log) Append(key, value string) (partition int, offset int64, err error) {
	// TODO(lab)
	return 0, 0, errNotImplemented
}

// AppendTo writes a message to an explicit partition (ErrNoSuchPartition if
// it does not exist).
func (l *Log) AppendTo(p int, key, value string) (int64, error) {
	// TODO(lab)
	return 0, errNotImplemented
}

// ApplyRetention enforces MaxMessages and MaxAge on every partition now.
func (l *Log) ApplyRetention() {
	// TODO(lab)
}

// StartOffset is the offset of the oldest retained message (EndOffset if the
// partition is empty).
func (l *Log) StartOffset(p int) (int64, error) {
	// TODO(lab)
	return 0, errNotImplemented
}

// EndOffset is the offset the next appended message will get.
func (l *Log) EndOffset(p int) (int64, error) {
	// TODO(lab)
	return 0, errNotImplemented
}

// Read returns up to max messages of partition p with Offset >= offset. The
// result must be a copy: callers must not be able to alter the log.
func (l *Log) Read(p int, offset int64, max int) ([]Message, error) {
	// TODO(lab)
	return nil, errNotImplemented
}

// Group is a consumer group: members share the partitions and the group
// keeps the committed offsets.
type Group struct {
	// TODO(lab): a mutex (one lock for the group and its consumers keeps this
	// simple), the generation, the members, the current assignment, the
	// committed offsets and the skipped counter.
}

// Group returns (creating if needed) the consumer group with the given id.
func (l *Log) Group(id string) *Group {
	// TODO(lab)
	return &Group{}
}

// Generation is the current rebalance generation (0 before anyone joined).
func (g *Group) Generation() int {
	// TODO(lab)
	return 0
}

// Members returns the member ids, sorted.
func (g *Group) Members() []string {
	// TODO(lab)
	return nil
}

// Committed is the offset of the next message the group will read from
// partition p after a restart (StartOffset if nothing was committed).
func (g *Group) Committed(p int) int64 {
	// TODO(lab)
	return 0
}

// Lag is how many messages of partition p the group has not committed past.
func (g *Group) Lag(p int) int64 {
	// TODO(lab)
	return 0
}

// TotalLag sums Lag over all partitions.
func (g *Group) TotalLag() int64 {
	// TODO(lab)
	return 0
}

// Skipped counts messages that retention deleted before the group read them.
func (g *Group) Skipped() int64 {
	// TODO(lab)
	return 0
}

// ResetOffset sets the committed offset of partition p (replay or skip).
// ErrOffsetOutOfRange outside [StartOffset, EndOffset].
func (g *Group) ResetOffset(p int, offset int64) error {
	// TODO(lab)
	return errNotImplemented
}

// Join adds a member and rebalances the group. The returned consumer is
// already in the new generation.
func (g *Group) Join(memberID string) (*Consumer, error) {
	// TODO(lab)
	return nil, errNotImplemented
}

// Evict removes a member on the coordinator's behalf (a missed heartbeat)
// and rebalances. It reports whether the member existed.
func (g *Group) Evict(memberID string) bool {
	// TODO(lab)
	return false
}

// Consumer is one member of a group.
type Consumer struct {
	// TODO(lab): the group, the id, the generation it synced to, its
	// assigned partitions and a read position per partition.
	id string
}

// ID is the member id.
func (c *Consumer) ID() string { return c.id }

// Generation is the generation this consumer last synced to.
func (c *Consumer) Generation() int {
	// TODO(lab)
	return 0
}

// Assignment returns the partitions assigned to this consumer as of its
// generation.
func (c *Consumer) Assignment() []int {
	// TODO(lab)
	return nil
}

// Rejoin adopts the group's current generation and assignment and rewinds
// read positions to the committed offsets. ErrFenced if no longer a member.
func (c *Consumer) Rejoin() error {
	// TODO(lab)
	return errNotImplemented
}

// Poll returns up to max messages, round-robin across assigned partitions.
// ErrFenced / ErrRebalance as documented above.
func (c *Consumer) Poll(max int) ([]Message, error) {
	// TODO(lab)
	return nil, errNotImplemented
}

// Commit records m as processed (m.Offset+1 becomes the committed offset).
func (c *Consumer) Commit(m Message) error {
	// TODO(lab)
	return errNotImplemented
}

// Close leaves the group (rebalance). Safe to call more than once.
func (c *Consumer) Close() {
	// TODO(lab)
}
