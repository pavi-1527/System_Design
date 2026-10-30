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

import (
	"errors"
	"sort"
)

// ErrNoRenditions is returned for an empty rendition list.
var ErrNoRenditions = errors.New("abr: no renditions")

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

func (p Policy) defaults() Policy {
	if p.Safety <= 0 {
		p.Safety = 0.8
	}
	if p.UpSwitchBufferSec <= 0 {
		p.UpSwitchBufferSec = 10
	}
	if p.PanicBufferSec <= 0 {
		p.PanicBufferSec = 4
	}
	return p
}

func sorted(rs []Rendition) []Rendition {
	out := append([]Rendition(nil), rs...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].BitrateBps < out[j].BitrateBps })
	return out
}

// candidate is the highest rendition affordable at throughput, or the lowest.
func (p Policy) candidate(rs []Rendition, throughputBps float64) Rendition {
	pick := rs[0]
	for _, r := range rs {
		if float64(r.BitrateBps) <= p.Safety*throughputBps {
			pick = r
		}
	}
	return pick
}

// Startup picks the first rendition to request.
func (p Policy) Startup(rs []Rendition, throughputBps float64) (Rendition, error) {
	if len(rs) == 0 {
		return Rendition{}, ErrNoRenditions
	}
	p = p.defaults()
	s := sorted(rs)
	if throughputBps <= 0 {
		return s[0], nil
	}
	return p.candidate(s, throughputBps), nil
}

// Next picks the rendition for the next segment.
func (p Policy) Next(rs []Rendition, st State) (Rendition, error) {
	if len(rs) == 0 {
		return Rendition{}, ErrNoRenditions
	}
	p = p.defaults()
	s := sorted(rs)
	curIdx := -1
	for i, r := range s {
		if r.Name == st.Current {
			curIdx = i
		}
	}
	if curIdx < 0 {
		return p.Startup(rs, st.ThroughputBps)
	}
	if st.BufferSec < p.PanicBufferSec {
		return s[0], nil
	}
	cand := p.candidate(s, st.ThroughputBps)
	candIdx := 0
	for i, r := range s {
		if r.Name == cand.Name {
			candIdx = i
		}
	}
	switch {
	case candIdx > curIdx && st.BufferSec >= p.UpSwitchBufferSec:
		return cand, nil
	case candIdx > curIdx:
		return s[curIdx], nil
	default:
		return cand, nil
	}
}
