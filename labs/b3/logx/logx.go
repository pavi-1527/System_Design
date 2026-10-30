// Package logx is a small structured logging library built from decorators:
// a Logger builds Entry values and hands them to a Sink, and Sinks compose.
//
// Lab B3 Unit 5 §2 (problem 11), time box 60 minutes. Work in stages and open
// the next stage's file only when you get there, like an interviewer who adds
// requirements as you go:
//
//	Stage 1 (≈ 25 min)  this file:  Logger, With, levels, LevelFilter, Multi, WriterSink
//	Stage 2 (≈ 20 min)  stage2.go:  AsyncSink: bounded queue, batching, overflow policy, Flush, Close
//	Stage 3 (≈ 15 min)  stage3.go:  retries, panic containment, a bounded Close, conservation
//
// Run one stage at a time:
//
//	go test -race -run 'Stage1' ./labs/b3/logx/
//
// Think about ownership before you write code: who owns an Entry once it has
// been handed to a sink, who owns a batch once it has been handed to the
// destination, and who closes what.
package logx

import (
	"errors"
	"io"
	"strconv"
	"time"
)

var errNotImplemented = errors.New("logx: not implemented")

// Level is a severity. The zero value is Debug.
type Level int

const (
	Debug Level = iota
	Info
	Warn
	Error
)

// String returns "DEBUG", "INFO", "WARN" or "ERROR" (given).
func (l Level) String() string {
	switch l {
	case Debug:
		return "DEBUG"
	case Info:
		return "INFO"
	case Warn:
		return "WARN"
	case Error:
		return "ERROR"
	}
	return "LEVEL(" + strconv.Itoa(int(l)) + ")"
}

// Field is one structured key/value pair.
type Field struct {
	Key   string
	Value any
}

// F is shorthand for Field{Key: key, Value: value} (given).
func F(key string, value any) Field { return Field{Key: key, Value: value} }

// Entry is one log record. Sinks must treat it as read-only. The Fields slice
// of an Entry produced by a Logger must be private to that entry: a sink may
// retain the Entry (for example in an async queue), and neither a later log
// call, a sibling logger, nor the caller's own slice may change it.
type Entry struct {
	Time   time.Time
	Level  Level
	Msg    string
	Fields []Field
}

// Sink consumes entries. Implementations must be safe for concurrent use.
type Sink interface {
	Write(Entry) error
}

// SinkFunc adapts a function to a Sink (given).
type SinkFunc func(Entry) error

func (f SinkFunc) Write(e Entry) error { return f(e) }

// Options configures a Logger.
type Options struct {
	// Level is the minimum level that is logged. The zero value (Debug) logs
	// everything.
	Level Level
	// Now supplies timestamps. nil means time.Now.
	Now func() time.Time
	// OnError is called with any error a sink returns. It runs on the
	// caller's goroutine. nil ignores sink errors: a logger never fails (or
	// panics, or blocks on) its caller because of a sink.
	OnError func(error)
}

// Logger turns calls into Entries and writes them to one Sink. It must be
// immutable after New, so that it is safe for concurrent use without a lock.
type Logger struct {
	// TODO(lab): sink, level, clock, error hook, fields attached by With.
}

// New returns a Logger writing to sink. A nil sink discards everything.
func New(opts Options, sink Sink) *Logger {
	// TODO(lab)
	_, _ = opts, sink
	return &Logger{}
}

// With returns a child Logger that adds fields to every entry. The parent is
// unchanged, and children of the same parent never see each other's fields
// (beware of append aliasing: a slice with spare capacity is shared state).
func (l *Logger) With(fields ...Field) *Logger {
	// TODO(lab)
	_ = fields
	return l
}

// Enabled reports whether an entry at level would be logged.
func (l *Logger) Enabled(level Level) bool {
	// TODO(lab)
	_ = level
	return false
}

// Log writes one entry. Below the minimum level it must return immediately,
// before reading the clock or copying any fields. Fields from With come
// first, then the call's own fields. The entry's Fields slice is a fresh copy.
func (l *Logger) Log(level Level, msg string, fields ...Field) {
	// TODO(lab)
	_, _, _ = level, msg, fields
}

func (l *Logger) Debug(msg string, fields ...Field) { l.Log(Debug, msg, fields...) }
func (l *Logger) Info(msg string, fields ...Field)  { l.Log(Info, msg, fields...) }
func (l *Logger) Warn(msg string, fields ...Field)  { l.Log(Warn, msg, fields...) }
func (l *Logger) Error(msg string, fields ...Field) { l.Log(Error, msg, fields...) }

// LevelFilter returns a Sink that forwards only entries at or above min.
func LevelFilter(min Level, next Sink) Sink {
	// TODO(lab)
	_, _ = min, next
	return SinkFunc(func(Entry) error { return errNotImplemented })
}

// Multi returns a Sink that writes every entry to every sink, in order. A
// failing sink does not stop the others; the returned error joins all
// failures (errors.Join), so errors.Is works on each of them. nil sinks are
// skipped.
func Multi(sinks ...Sink) Sink {
	// TODO(lab)
	_ = sinks
	return SinkFunc(func(Entry) error { return errNotImplemented })
}

// WriterSink formats entries as one line each and writes them to an io.Writer.
//
// Line format: "<RFC 3339 UTC time> <LEVEL> <msg> key=value key=value\n".
// A value (fmt.Sprint of the field value) is quoted with strconv.Quote when
// it is empty or contains a space, '"', '=' or a control character. The
// message is written as is unless it contains a newline or carriage return,
// in which case it is quoted: one entry is always exactly one line (otherwise
// a user-controlled string could forge log lines).
type WriterSink struct {
	// TODO(lab): the writer, and whatever makes concurrent Writes safe.
}

// NewWriterSink returns a WriterSink. Each entry must reach w in a single
// w.Write call made while holding a mutex, so lines never interleave even if
// w itself is not safe for concurrent use.
func NewWriterSink(w io.Writer) *WriterSink {
	// TODO(lab)
	_ = w
	return &WriterSink{}
}

func (s *WriterSink) Write(e Entry) error {
	// TODO(lab)
	_ = e
	return errNotImplemented
}
