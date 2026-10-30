// Package abr is the client side of adaptive bitrate streaming: given the
// renditions a manifest offers, the throughput the client has measured and
// how much video is buffered, pick the rendition for the next segment.
//
// It is the STRETCH lab for C11 Unit 2 §7 (not gated). Run:
// go test -race ./labs/c11/abr/
//
// Contract (the tests depend on every line):
//   - Renditions are given in any order; the policy sorts them by bitrate
//     ascending. An empty list is ErrNoRenditions.
//   - Startup(rs, throughputBps) picks the highest rendition whose bitrate
//     is at most Safety × throughput, or the lowest rendition if none fits
//     (or if throughput is unknown, i.e. <= 0).
//   - Next(rs, state) applies hysteresis so the picture does not flap:
//     candidate = the highest rendition whose bitrate <= Safety × throughput
//     (the lowest if none fits). If BufferSec < PanicBufferSec, return the
//     lowest rendition regardless of throughput (rebuffering is the worst
//     outcome). If the candidate is above the current rendition, switch up
//     only when BufferSec >= UpSwitchBufferSec; otherwise keep the current
//     one. If the candidate is below the current rendition, switch down
//     immediately. If state.Current names no rendition, behave like Startup.
//   - Policy zero values take the defaults: Safety 0.8, UpSwitchBufferSec 10,
//     PanicBufferSec 4.
package abr

import "errors"

// ErrNoRenditions is returned for an empty rendition list.
var ErrNoRenditions = errors.New("abr: no renditions")

var errNotImplemented = errors.New("abr: not implemented")

// Rendition is one ladder rung.
type Rendition struct {
	Name       string
	BitrateBps int
}

// State is what the player knows when it chooses the next segment.
type State struct {
	Current       string  // rendition of the previous segment ("" at startup)
	ThroughputBps float64 // measured download throughput (<= 0 if unknown)
	BufferSec     float64 // seconds of video buffered ahead of playback
}

// Policy tunes the selection. Zero values take the defaults.
type Policy struct {
	Safety            float64 // fraction of measured throughput to spend; default 0.8
	UpSwitchBufferSec float64 // buffer needed before switching up; default 10
	PanicBufferSec    float64 // below this, drop to the lowest rendition; default 4
}

// Startup picks the first rendition to request.
func (p Policy) Startup(rs []Rendition, throughputBps float64) (Rendition, error) {
	// TODO(lab)
	_, _ = rs, throughputBps
	return Rendition{}, errNotImplemented
}

// Next picks the rendition for the next segment.
func (p Policy) Next(rs []Rendition, st State) (Rendition, error) {
	// TODO(lab)
	_, _ = rs, st
	return Rendition{}, errNotImplemented
}
