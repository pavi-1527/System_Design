// Package logx is a small structured logging library built from decorators:
// a Logger builds Entry values and hands them to a Sink, and Sinks compose.
//
// Lab B3 Unit 5 §2 (problem 11). Stage 1 is this file; Stage 2 (an async,
// batching sink with a backpressure policy) lives in stage2.go and Stage 3
// (failure isolation and a bounded shutdown) in stage3.go.
//
// Reference design: a Logger is immutable after New (so it is safe to share
// and needs no lock); every Log call builds a fresh Entry with its own Fields
// slice (so a sink may keep it); Sinks are small interfaces that wrap each
// other: LevelFilter, Multi, WriterSink, and later AsyncSink.
package logx

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Level is a severity. The zero value is Debug.
type Level int

const (
	Debug Level = iota
	Info
	Warn
	Error
)

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

// F is shorthand for Field{Key: key, Value: value}.
func F(key string, value any) Field { return Field{Key: key, Value: value} }

// Entry is one log record. Sinks must treat it as read-only. The Fields slice
// of an Entry produced by a Logger is private to that entry, so a sink may
// retain the Entry (for example in an async queue) without copying it.
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

// Options configures a Logger.
type Options struct {
	// Level is the minimum level that is logged. The zero value (Debug) logs
	// everything.
	Level Level
	// Now supplies timestamps. nil means time.Now.
	Now func() time.Time
	// OnError is called with any error a sink returns. It runs on the
	// caller's goroutine and must be fast and safe for concurrent use. nil
	// ignores sink errors: a logger never fails its caller.
	OnError func(error)
}

// Logger turns calls into Entries and writes them to one Sink. It is
// immutable after New, so it is safe for concurrent use and cheap to share.
type Logger struct {
	sink    Sink
	level   Level
	now     func() time.Time
	onError func(error)
	fields  []Field // fields attached by With; never appended to in place
}

// New returns a Logger writing to sink. A nil sink discards everything.
func New(opts Options, sink Sink) *Logger {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Logger{sink: sink, level: opts.Level, now: opts.Now, onError: opts.OnError}
}

// With returns a child Logger that adds fields to every entry. The parent is
// unchanged, and children of the same parent never see each other's fields.
func (l *Logger) With(fields ...Field) *Logger {
	child := *l
	child.fields = make([]Field, 0, len(l.fields)+len(fields))
	child.fields = append(child.fields, l.fields...)
	child.fields = append(child.fields, fields...)
	return &child
}

// Enabled reports whether an entry at level would be logged.
func (l *Logger) Enabled(level Level) bool { return level >= l.level }

// Log writes one entry. Below the minimum level it returns immediately,
// before reading the clock or copying any fields. Fields from With come
// first, then the call's own fields.
func (l *Logger) Log(level Level, msg string, fields ...Field) {
	if !l.Enabled(level) || l.sink == nil {
		return
	}
	e := Entry{Time: l.now(), Level: level, Msg: msg}
	if n := len(l.fields) + len(fields); n > 0 {
		e.Fields = make([]Field, 0, n)
		e.Fields = append(e.Fields, l.fields...)
		e.Fields = append(e.Fields, fields...)
	}
	if err := l.sink.Write(e); err != nil && l.onError != nil {
		l.onError(err)
	}
}

func (l *Logger) Debug(msg string, fields ...Field) { l.Log(Debug, msg, fields...) }
func (l *Logger) Info(msg string, fields ...Field)  { l.Log(Info, msg, fields...) }
func (l *Logger) Warn(msg string, fields ...Field)  { l.Log(Warn, msg, fields...) }
func (l *Logger) Error(msg string, fields ...Field) { l.Log(Error, msg, fields...) }

// SinkFunc adapts a function to a Sink.
type SinkFunc func(Entry) error

func (f SinkFunc) Write(e Entry) error { return f(e) }

// LevelFilter returns a Sink that forwards only entries at or above min.
func LevelFilter(min Level, next Sink) Sink {
	return SinkFunc(func(e Entry) error {
		if e.Level < min {
			return nil
		}
		return next.Write(e)
	})
}

// Multi returns a Sink that writes every entry to every sink, in order. A
// failing sink does not stop the others; the returned error joins all
// failures (errors.Join), so errors.Is works on each of them. nil sinks are
// skipped.
func Multi(sinks ...Sink) Sink {
	list := make([]Sink, 0, len(sinks))
	for _, s := range sinks {
		if s != nil {
			list = append(list, s)
		}
	}
	return SinkFunc(func(e Entry) error {
		var errs []error
		for _, s := range list {
			if err := s.Write(e); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	})
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
	mu sync.Mutex
	w  io.Writer
}

// NewWriterSink returns a WriterSink. Each entry is written with a single
// w.Write call made while holding a mutex, so lines never interleave even if
// w itself is not safe for concurrent use.
func NewWriterSink(w io.Writer) *WriterSink { return &WriterSink{w: w} }

func needsQuote(s string) bool {
	return s == "" || strings.ContainsFunc(s, func(r rune) bool {
		return r <= ' ' || r == '"' || r == '=' || r == 0x7f
	})
}

func quoteIfNeeded(s string) string {
	if needsQuote(s) {
		return strconv.Quote(s)
	}
	return s
}

func formatLine(e Entry) string {
	var b strings.Builder
	b.WriteString(e.Time.UTC().Format(time.RFC3339))
	b.WriteByte(' ')
	b.WriteString(e.Level.String())
	b.WriteByte(' ')
	if strings.ContainsAny(e.Msg, "\n\r") {
		b.WriteString(strconv.Quote(e.Msg))
	} else {
		b.WriteString(e.Msg)
	}
	for _, f := range e.Fields {
		b.WriteByte(' ')
		b.WriteString(quoteIfNeeded(f.Key))
		b.WriteByte('=')
		b.WriteString(quoteIfNeeded(fmt.Sprint(f.Value)))
	}
	b.WriteByte('\n')
	return b.String()
}

func (s *WriterSink) Write(e Entry) error {
	line := formatLine(e)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := io.WriteString(s.w, line)
	return err
}
