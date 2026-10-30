// Package chat is the lab for C6 (chat / messaging): the conversation core of
// a messaging service, in memory. It is the part of the design that interviews
// probe hardest, because every guarantee a chat product makes lives here:
//
//   - every message in a conversation gets a dense, per-conversation sequence
//     number (1, 2, 3, ...) assigned by a single writer, so order is total and
//     a client can detect gaps;
//   - a send is idempotent per (sender, client message id), so a retried send
//     after a timeout never produces a second message;
//   - a client that was offline catches up with Sync(afterSeq), which returns
//     exactly the messages it has not seen, in order;
//   - online members get live deliveries over a bounded subscription; a slow
//     subscriber never slows a sender down, it just has to resync;
//   - read receipts are per-(conversation, user) cursors that only move forward.
//
// Everything is safe for concurrent use. Reference solution.
package chat

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Limits.
const (
	// DefaultSubscriberBuffer is the per-subscription channel capacity when
	// Options.SubscriberBuffer is zero.
	DefaultSubscriberBuffer = 64
	// MaxSyncLimit is the largest page Sync will return.
	MaxSyncLimit = 100
)

// Errors returned by the service. Compare with errors.Is.
var (
	// ErrUnknownConversation: no such conversation (nobody has joined it).
	ErrUnknownConversation = errors.New("chat: unknown conversation")
	// ErrNotMember: the user is not a member of the conversation.
	ErrNotMember = errors.New("chat: not a member of the conversation")
	// ErrInvalidArgument: an empty conversation id, user id, client message id or body.
	ErrInvalidArgument = errors.New("chat: invalid argument")
	// ErrInvalidLimit: Sync limit outside [1, MaxSyncLimit].
	ErrInvalidLimit = errors.New("chat: invalid limit")
	// ErrInvalidSeq: MarkRead with a seq beyond the conversation's latest message.
	ErrInvalidSeq = errors.New("chat: seq beyond the latest message")
)

// Message is one stored message. Seq is dense per conversation: the n-th
// message ever stored in a conversation has Seq == n.
type Message struct {
	Conversation string    `json:"conversation"`
	Seq          uint64    `json:"seq"`
	Sender       string    `json:"sender"`
	ClientMsgID  string    `json:"client_msg_id"`
	Body         string    `json:"body"`
	SentAt       time.Time `json:"sent_at"`
}

// Options configures a Service.
type Options struct {
	// SubscriberBuffer is the capacity of every subscription's channel.
	// Zero means DefaultSubscriberBuffer.
	SubscriberBuffer int
	// Now is the clock used for Message.SentAt. Nil means time.Now.
	Now func() time.Time
}

// conversation is the single writer for one conversation: its lock serialises
// sequence assignment, the append to the log and the fan-out, which is what
// makes the order total and identical on every subscription.
type conversation struct {
	mu      sync.RWMutex
	members map[string]struct{}
	log     []Message         // log[i].Seq == uint64(i)+1
	dedupe  map[string]uint64 // sender + "\x00" + clientMsgID → seq
	read    map[string]uint64 // user → read cursor (highest seq marked read)
}

func newConversation() *conversation {
	return &conversation{
		members: map[string]struct{}{},
		dedupe:  map[string]uint64{},
		read:    map[string]uint64{},
	}
}

// Service is the conversation core. The zero value is not usable; use New.
type Service struct {
	buf int
	now func() time.Time

	mu     sync.RWMutex
	convos map[string]*conversation
	subs   map[string]map[*Subscription]struct{} // user → live subscriptions
}

// New returns an empty service.
func New(opt Options) *Service {
	buf := opt.SubscriberBuffer
	if buf <= 0 {
		buf = DefaultSubscriberBuffer
	}
	now := opt.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		buf:    buf,
		now:    now,
		convos: map[string]*conversation{},
		subs:   map[string]map[*Subscription]struct{}{},
	}
}

// Join adds user to the conversation, creating the conversation if it does not
// exist yet. Joining twice is a no-op.
func (s *Service) Join(convo, user string) error {
	if convo == "" || user == "" {
		return ErrInvalidArgument
	}
	s.mu.Lock()
	c, ok := s.convos[convo]
	if !ok {
		c = newConversation()
		s.convos[convo] = c
	}
	s.mu.Unlock()
	c.mu.Lock()
	c.members[user] = struct{}{}
	c.mu.Unlock()
	return nil
}

func (s *Service) lookup(convo string) (*conversation, error) {
	s.mu.RLock()
	c, ok := s.convos[convo]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrUnknownConversation
	}
	return c, nil
}

// Members returns the conversation's members, sorted.
func (s *Service) Members(convo string) ([]string, error) {
	c, err := s.lookup(convo)
	if err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.members))
	for u := range c.members {
		out = append(out, u)
	}
	sort.Strings(out)
	return out, nil
}

// Latest returns the highest Seq stored in the conversation (0 if none).
func (s *Service) Latest(convo string) (uint64, error) {
	c, err := s.lookup(convo)
	if err != nil {
		return 0, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return uint64(len(c.log)), nil
}

// Send stores a message and delivers it to every live subscription of every
// member (including the sender's own subscriptions: their other devices).
//
// It is idempotent per (sender, clientMsgID) within a conversation: a repeat
// returns the message stored the first time, with the same Seq, and nothing is
// stored or delivered again. Seqs are dense: concurrent senders get distinct
// consecutive numbers with no gaps. A sender must be a member. A context that
// is already done fails without storing anything.
func (s *Service) Send(ctx context.Context, convo, sender, clientMsgID, body string) (Message, error) {
	if convo == "" || sender == "" || clientMsgID == "" || body == "" {
		return Message{}, ErrInvalidArgument
	}
	if err := ctx.Err(); err != nil {
		return Message{}, err
	}
	c, err := s.lookup(convo)
	if err != nil {
		return Message{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.members[sender]; !ok {
		return Message{}, ErrNotMember
	}
	key := sender + "\x00" + clientMsgID
	if seq, dup := c.dedupe[key]; dup {
		return c.log[seq-1], nil
	}
	m := Message{
		Conversation: convo,
		Seq:          uint64(len(c.log)) + 1,
		Sender:       sender,
		ClientMsgID:  clientMsgID,
		Body:         body,
		SentAt:       s.now(),
	}
	c.log = append(c.log, m)
	c.dedupe[key] = m.Seq
	// Fan out while still holding c.mu: seq n reaches every subscription before
	// seq n+1 is even assigned, so per-conversation order holds on every channel.
	s.fanout(c, m)
	return m, nil
}

// fanout offers m to every live subscription of every member. Non-blocking:
// a full subscription drops the message (see Subscription).
func (s *Service) fanout(c *conversation, m Message) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for user := range c.members {
		for sub := range s.subs[user] {
			sub.offer(m)
		}
	}
}

// Sync returns up to limit messages with Seq > afterSeq, in Seq order: what a
// reconnecting client is missing. The result is a copy and never nil. The user
// must be a member. limit must be in [1, MaxSyncLimit].
func (s *Service) Sync(convo, user string, afterSeq uint64, limit int) ([]Message, error) {
	if convo == "" || user == "" {
		return nil, ErrInvalidArgument
	}
	if limit < 1 || limit > MaxSyncLimit {
		return nil, ErrInvalidLimit
	}
	c, err := s.lookup(convo)
	if err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if _, ok := c.members[user]; !ok {
		return nil, ErrNotMember
	}
	n := uint64(len(c.log))
	if afterSeq >= n {
		return []Message{}, nil
	}
	end := afterSeq + uint64(limit)
	if end > n {
		end = n
	}
	out := make([]Message, end-afterSeq)
	copy(out, c.log[afterSeq:end])
	return out, nil
}

// MarkRead records that user has read everything up to and including seq and
// returns the resulting read cursor. The cursor never moves backwards: a
// smaller seq (a late receipt from another device) leaves it unchanged. seq
// beyond the latest message is rejected with ErrInvalidSeq.
func (s *Service) MarkRead(convo, user string, seq uint64) (uint64, error) {
	if convo == "" || user == "" {
		return 0, ErrInvalidArgument
	}
	c, err := s.lookup(convo)
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.members[user]; !ok {
		return 0, ErrNotMember
	}
	if seq > uint64(len(c.log)) {
		return c.read[user], ErrInvalidSeq
	}
	if seq > c.read[user] {
		c.read[user] = seq
	}
	return c.read[user], nil
}

// Unread returns how many messages in the conversation are beyond the user's
// read cursor.
func (s *Service) Unread(convo, user string) (uint64, error) {
	if convo == "" || user == "" {
		return 0, ErrInvalidArgument
	}
	c, err := s.lookup(convo)
	if err != nil {
		return 0, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if _, ok := c.members[user]; !ok {
		return 0, ErrNotMember
	}
	return uint64(len(c.log)) - c.read[user], nil
}

// Subscription is one live connection of one user: the in-memory stand-in for
// a WebSocket held by a gateway server.
//
// Delivery policy: offers are non-blocking. When the buffer is full the
// message is dropped for this subscription, Dropped is incremented, and the
// message is never delivered on this subscription later; the client must Sync
// from its last seen Seq. The log is the truth and the push is a hint, which
// is exactly how a gateway treats a slow socket.
type Subscription struct {
	// C delivers messages from every conversation the user is a member of,
	// in Seq order per conversation. It is closed by Close or when the
	// Connect context is done.
	C <-chan Message

	ch      chan Message
	user    string
	svc     *Service
	once    sync.Once
	dropped atomic.Uint64

	mu   sync.Mutex
	stop func() bool // cancels the context.AfterFunc hook
}

// Connect registers a live subscription for user (who need not be a member of
// anything yet). The subscription closes itself when ctx is done. A context
// that is already done fails. Pattern for clients: Connect first, then Sync
// from the last seen Seq, and dedupe by Seq.
func (s *Service) Connect(ctx context.Context, user string) (*Subscription, error) {
	if user == "" {
		return nil, ErrInvalidArgument
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ch := make(chan Message, s.buf)
	sub := &Subscription{C: ch, ch: ch, user: user, svc: s}
	s.mu.Lock()
	if s.subs[user] == nil {
		s.subs[user] = map[*Subscription]struct{}{}
	}
	s.subs[user][sub] = struct{}{}
	s.mu.Unlock()
	sub.mu.Lock()
	sub.stop = context.AfterFunc(ctx, sub.Close)
	sub.mu.Unlock()
	return sub, nil
}

func (sub *Subscription) offer(m Message) {
	select {
	case sub.ch <- m:
	default:
		sub.dropped.Add(1)
	}
}

// Dropped reports how many messages were dropped because the buffer was full.
func (sub *Subscription) Dropped() uint64 { return sub.dropped.Load() }

// Close unregisters the subscription and closes C. Safe to call more than
// once and concurrently with Send; buffered messages stay readable until
// drained.
func (sub *Subscription) Close() {
	sub.once.Do(func() {
		sub.mu.Lock()
		stop := sub.stop
		sub.mu.Unlock()
		if stop != nil {
			stop()
		}
		s := sub.svc
		s.mu.Lock()
		if m := s.subs[sub.user]; m != nil {
			delete(m, sub)
			if len(m) == 0 {
				delete(s.subs, sub.user)
			}
		}
		s.mu.Unlock()
		// No Send can be offering to sub any more: offers happen under s.mu.RLock
		// and the removal above happened under s.mu.Lock.
		close(sub.ch)
	})
}
