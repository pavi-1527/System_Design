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
// Everything must be safe for concurrent use (the tests run under -race).
package chat

import (
	"context"
	"errors"
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

var errNotImplemented = errors.New("chat: not implemented")

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

// Service is the conversation core. The zero value is not usable; use New.
//
// Suggested shape: one struct per conversation holding its own lock, the
// member set, an append-only log where log[i].Seq == i+1, a dedupe map
// (sender + client message id → seq) and the read cursors; plus a
// service-level registry of live subscriptions keyed by user. Assigning the
// seq, appending, recording the dedupe entry and fanning out all happen under
// the conversation's lock: that single writer is what makes the order total.
type Service struct {
	buf int
	now func() time.Time
	// TODO(lab): conversations keyed by id, and the subscription registry.
}

// New returns an empty service. Provided.
func New(opt Options) *Service {
	buf := opt.SubscriberBuffer
	if buf <= 0 {
		buf = DefaultSubscriberBuffer
	}
	now := opt.Now
	if now == nil {
		now = time.Now
	}
	return &Service{buf: buf, now: now}
}

// Join adds user to the conversation, creating the conversation if it does not
// exist yet. Joining twice is a no-op. Empty ids are ErrInvalidArgument.
func (s *Service) Join(convo, user string) error {
	// TODO(lab)
	return errNotImplemented
}

// Members returns the conversation's members, sorted.
func (s *Service) Members(convo string) ([]string, error) {
	// TODO(lab)
	return nil, errNotImplemented
}

// Latest returns the highest Seq stored in the conversation (0 if none).
func (s *Service) Latest(convo string) (uint64, error) {
	// TODO(lab)
	return 0, errNotImplemented
}

// Send stores a message and delivers it to every live subscription of every
// member (including the sender's own subscriptions: their other devices).
//
// It is idempotent per (sender, clientMsgID) within a conversation: a repeat
// returns the message stored the first time, with the same Seq, and nothing is
// stored or delivered again. Seqs are dense: concurrent senders get distinct
// consecutive numbers with no gaps. A sender must be a member. A context that
// is already done fails without storing anything. Empty convo, sender,
// clientMsgID or body is ErrInvalidArgument.
func (s *Service) Send(ctx context.Context, convo, sender, clientMsgID, body string) (Message, error) {
	// TODO(lab): validate, check ctx.Err(), look up the conversation, then under
	// its lock: membership, dedupe, seq = len(log)+1, append, record the dedupe
	// entry, fan out to the members' subscriptions (non-blocking offers).
	return Message{}, errNotImplemented
}

// Sync returns up to limit messages with Seq > afterSeq, in Seq order: what a
// reconnecting client is missing. The result is a copy and never nil. The user
// must be a member. limit must be in [1, MaxSyncLimit] (ErrInvalidLimit).
func (s *Service) Sync(convo, user string, afterSeq uint64, limit int) ([]Message, error) {
	// TODO(lab): because seqs are dense, log[afterSeq:] is the answer; copy it.
	return nil, errNotImplemented
}

// MarkRead records that user has read everything up to and including seq and
// returns the resulting read cursor. The cursor never moves backwards: a
// smaller seq (a late receipt from another device) leaves it unchanged. seq
// beyond the latest message is rejected with ErrInvalidSeq.
func (s *Service) MarkRead(convo, user string, seq uint64) (uint64, error) {
	// TODO(lab)
	return 0, errNotImplemented
}

// Unread returns how many messages in the conversation are beyond the user's
// read cursor.
func (s *Service) Unread(convo, user string) (uint64, error) {
	// TODO(lab)
	return 0, errNotImplemented
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
	// TODO(lab): the send side of C, the owning service and user, a sync.Once
	// for Close, a dropped counter, and whatever Close needs to stop the
	// context hook (context.AfterFunc returns a stop func; a goroutine that
	// selects on ctx.Done() needs a done channel so it exits on Close too).
}

// Connect registers a live subscription for user (who need not be a member of
// anything yet). The subscription closes itself when ctx is done. A context
// that is already done fails with its error. Empty user is ErrInvalidArgument.
// Pattern for clients: Connect first, then Sync from the last seen Seq, and
// dedupe by Seq.
func (s *Service) Connect(ctx context.Context, user string) (*Subscription, error) {
	// TODO(lab): make the buffered channel (capacity s.buf), register the
	// subscription under the user, hook ctx to Close.
	return nil, errNotImplemented
}

// Dropped reports how many messages were dropped because the buffer was full.
func (sub *Subscription) Dropped() uint64 {
	// TODO(lab)
	return 0
}

// Close unregisters the subscription and closes C. Safe to call more than
// once and concurrently with Send; buffered messages stay readable until
// drained. Make sure no Send can still be offering to the channel when you
// close it (a send on a closed channel panics).
func (sub *Subscription) Close() {
	// TODO(lab)
}
