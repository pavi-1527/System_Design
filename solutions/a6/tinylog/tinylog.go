// Package tinylog is a tiny in-memory partitioned append-only log with
// consumer groups, in the style of Kafka. It is the lab for A6 Unit 1.
//
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
	"hash/fnv"
	"sort"
	"sync"
	"time"
)

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
	Hash        func(key string) uint32 // default FNV-1a 32-bit
}

type partition struct {
	base int64 // offset of msgs[0]
	msgs []Message
}

// Log is a partitioned append-only log.
type Log struct {
	cfg    Config
	mu     sync.Mutex
	parts  []*partition
	rr     int
	groups map[string]*Group
}

// New returns an empty Log.
func New(cfg Config) *Log {
	if cfg.Partitions < 1 {
		cfg.Partitions = 1
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Hash == nil {
		cfg.Hash = func(key string) uint32 {
			h := fnv.New32a()
			h.Write([]byte(key))
			return h.Sum32()
		}
	}
	l := &Log{cfg: cfg, groups: map[string]*Group{}}
	for i := 0; i < cfg.Partitions; i++ {
		l.parts = append(l.parts, &partition{})
	}
	return l
}

// Partitions returns the number of partitions.
func (l *Log) Partitions() int { return l.cfg.Partitions }

// PartitionFor returns the partition a non-empty key maps to.
func (l *Log) PartitionFor(key string) int {
	return int(l.cfg.Hash(key) % uint32(l.cfg.Partitions))
}

// Append writes a message and returns where it landed.
func (l *Log) Append(key, value string) (partition int, offset int64, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if key == "" {
		partition = l.rr % len(l.parts)
		l.rr++
	} else {
		partition = l.PartitionFor(key)
	}
	return partition, l.appendLocked(partition, key, value), nil
}

// AppendTo writes a message to an explicit partition.
func (l *Log) AppendTo(p int, key, value string) (int64, error) {
	if p < 0 || p >= len(l.parts) {
		return 0, ErrNoSuchPartition
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.appendLocked(p, key, value), nil
}

func (l *Log) appendLocked(p int, key, value string) int64 {
	pt := l.parts[p]
	off := pt.base + int64(len(pt.msgs))
	pt.msgs = append(pt.msgs, Message{Partition: p, Offset: off, Key: key, Value: value, Time: l.cfg.Now()})
	l.enforceLocked(pt)
	return off
}

func (l *Log) enforceLocked(pt *partition) {
	drop := 0
	if l.cfg.MaxMessages > 0 && len(pt.msgs) > l.cfg.MaxMessages {
		drop = len(pt.msgs) - l.cfg.MaxMessages
	}
	if l.cfg.MaxAge > 0 {
		cutoff := l.cfg.Now().Add(-l.cfg.MaxAge)
		for drop < len(pt.msgs) && pt.msgs[drop].Time.Before(cutoff) {
			drop++
		}
	}
	if drop > 0 {
		pt.msgs = append([]Message(nil), pt.msgs[drop:]...)
		pt.base += int64(drop)
	}
}

// ApplyRetention enforces MaxMessages and MaxAge on every partition now.
func (l *Log) ApplyRetention() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, pt := range l.parts {
		l.enforceLocked(pt)
	}
}

// StartOffset is the offset of the oldest retained message (or EndOffset if
// the partition is empty).
func (l *Log) StartOffset(p int) (int64, error) {
	if p < 0 || p >= len(l.parts) {
		return 0, ErrNoSuchPartition
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.parts[p].base, nil
}

// EndOffset is the offset the next appended message will get.
func (l *Log) EndOffset(p int) (int64, error) {
	if p < 0 || p >= len(l.parts) {
		return 0, ErrNoSuchPartition
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	pt := l.parts[p]
	return pt.base + int64(len(pt.msgs)), nil
}

// Read returns up to max messages of partition p starting at offset.
func (l *Log) Read(p int, offset int64, max int) ([]Message, error) {
	if p < 0 || p >= len(l.parts) {
		return nil, ErrNoSuchPartition
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	pt := l.parts[p]
	end := pt.base + int64(len(pt.msgs))
	if offset < pt.base || offset > end {
		return nil, ErrOffsetOutOfRange
	}
	if max <= 0 {
		return nil, nil
	}
	i := int(offset - pt.base)
	j := i + max
	if j > len(pt.msgs) {
		j = len(pt.msgs)
	}
	return append([]Message(nil), pt.msgs[i:j]...), nil
}

// Group is a consumer group: members share the partitions and the group
// keeps the committed offsets.
type Group struct {
	id  string
	log *Log

	mu         sync.Mutex
	generation int
	members    map[string]*Consumer
	assignment map[string][]int
	committed  map[int]int64
	skipped    int64
}

// Group returns (creating if needed) the consumer group with the given id.
func (l *Log) Group(id string) *Group {
	l.mu.Lock()
	defer l.mu.Unlock()
	g, ok := l.groups[id]
	if !ok {
		g = &Group{id: id, log: l, members: map[string]*Consumer{}, assignment: map[string][]int{}, committed: map[int]int64{}}
		l.groups[id] = g
	}
	return g
}

// Generation is the current rebalance generation (0 before anyone joined).
func (g *Group) Generation() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.generation
}

// Members returns the member ids, sorted.
func (g *Group) Members() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.memberIDsLocked()
}

func (g *Group) memberIDsLocked() []string {
	ids := make([]string, 0, len(g.members))
	for id := range g.members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Committed is the offset of the next message the group will read from
// partition p after a restart.
func (g *Group) Committed(p int) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.committedLocked(p)
}

func (g *Group) committedLocked(p int) int64 {
	if v, ok := g.committed[p]; ok {
		return v
	}
	start, _ := g.log.StartOffset(p)
	return start
}

// Lag is how many messages of partition p the group has not committed past.
func (g *Group) Lag(p int) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lagLocked(p)
}

func (g *Group) lagLocked(p int) int64 {
	end, err := g.log.EndOffset(p)
	if err != nil {
		return 0
	}
	c := g.committedLocked(p)
	if start, _ := g.log.StartOffset(p); c < start {
		c = start
	}
	return end - c
}

// TotalLag sums Lag over all partitions.
func (g *Group) TotalLag() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	var n int64
	for p := 0; p < g.log.Partitions(); p++ {
		n += g.lagLocked(p)
	}
	return n
}

// Skipped counts messages that retention deleted before the group read them.
func (g *Group) Skipped() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.skipped
}

// ResetOffset sets the committed offset of partition p (replay or skip).
func (g *Group) ResetOffset(p int, offset int64) error {
	start, err := g.log.StartOffset(p)
	if err != nil {
		return err
	}
	end, _ := g.log.EndOffset(p)
	if offset < start || offset > end {
		return ErrOffsetOutOfRange
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.committed[p] = offset
	return nil
}

// Join adds a member and rebalances the group.
func (g *Group) Join(memberID string) (*Consumer, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, dup := g.members[memberID]; dup {
		return nil, ErrDuplicateMember
	}
	c := &Consumer{g: g, id: memberID}
	g.members[memberID] = c
	g.rebalanceLocked()
	c.syncLocked()
	return c, nil
}

// Evict removes a member on the coordinator's behalf (a missed heartbeat).
func (g *Group) Evict(memberID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.members[memberID]; !ok {
		return false
	}
	delete(g.members, memberID)
	g.rebalanceLocked()
	return true
}

func (g *Group) rebalanceLocked() {
	g.generation++
	g.assignment = map[string][]int{}
	ids := g.memberIDsLocked()
	if len(ids) == 0 {
		return
	}
	for p := 0; p < g.log.Partitions(); p++ {
		owner := ids[p%len(ids)]
		g.assignment[owner] = append(g.assignment[owner], p)
	}
}

// Consumer is one member of a group.
type Consumer struct {
	g          *Group
	id         string
	generation int
	parts      []int
	pos        map[int]int64
}

func (c *Consumer) syncLocked() {
	c.generation = c.g.generation
	c.parts = append([]int(nil), c.g.assignment[c.id]...)
	c.pos = map[int]int64{}
	for _, p := range c.parts {
		c.pos[p] = c.g.committedLocked(p)
	}
}

func (c *Consumer) memberLocked() bool { return c.g.members[c.id] == c }

// ID is the member id.
func (c *Consumer) ID() string { return c.id }

// Generation is the generation this consumer last synced to.
func (c *Consumer) Generation() int {
	c.g.mu.Lock()
	defer c.g.mu.Unlock()
	return c.generation
}

// Assignment returns the partitions assigned to this consumer as of its
// generation.
func (c *Consumer) Assignment() []int {
	c.g.mu.Lock()
	defer c.g.mu.Unlock()
	return append([]int(nil), c.parts...)
}

// Rejoin adopts the group's current generation and assignment and rewinds
// read positions to the committed offsets.
func (c *Consumer) Rejoin() error {
	c.g.mu.Lock()
	defer c.g.mu.Unlock()
	if !c.memberLocked() {
		return ErrFenced
	}
	c.syncLocked()
	return nil
}

// Poll returns up to max messages. See the package documentation.
func (c *Consumer) Poll(max int) ([]Message, error) {
	g := c.g
	g.mu.Lock()
	defer g.mu.Unlock()
	if !c.memberLocked() {
		return nil, ErrFenced
	}
	if c.generation != g.generation {
		return nil, ErrRebalance
	}
	var out []Message
	for len(out) < max {
		progressed := false
		for _, p := range c.parts {
			if len(out) >= max {
				break
			}
			pos := c.pos[p]
			if start, _ := g.log.StartOffset(p); pos < start {
				g.skipped += start - pos
				pos = start
				c.pos[p] = pos
			}
			msgs, err := g.log.Read(p, pos, 1)
			if err != nil || len(msgs) == 0 {
				continue
			}
			out = append(out, msgs[0])
			c.pos[p] = pos + 1
			progressed = true
		}
		if !progressed {
			break
		}
	}
	return out, nil
}

// Commit records m as processed. See the package documentation.
func (c *Consumer) Commit(m Message) error {
	g := c.g
	g.mu.Lock()
	defer g.mu.Unlock()
	if !c.memberLocked() {
		return ErrFenced
	}
	if c.generation != g.generation {
		return ErrStaleGeneration
	}
	owned := false
	for _, p := range c.parts {
		if p == m.Partition {
			owned = true
		}
	}
	if !owned {
		return ErrNotAssigned
	}
	if next := m.Offset + 1; next > g.committedLocked(m.Partition) {
		g.committed[m.Partition] = next
	}
	return nil
}

// Close leaves the group. Safe to call more than once.
func (c *Consumer) Close() {
	g := c.g
	g.mu.Lock()
	defer g.mu.Unlock()
	if !c.memberLocked() {
		return
	}
	delete(g.members, c.id)
	g.rebalanceLocked()
}
