// Package review implements spaced repetition over quiz questions using Leitner boxes.
// Box b means "next review after intervals[b] days"; a correct review moves a card up
// one box, a miss sends it back to box 0. Quiz attempts enroll questions automatically.
package review

import (
	"math/rand/v2"
	"sort"
	"time"

	"github.com/chethu007/system-design-course/internal/course"
	"github.com/chethu007/system-design-course/internal/progress"
)

// Enroll records a quiz answer against the deck: new questions enter box 1 if answered
// correctly (box 0 otherwise); a miss on an existing card resets it to box 0.
// A correct quiz answer on an existing card leaves its schedule alone, so taking
// quizzes never lets cards skip review.
func Enroll(p *progress.Progress, pol course.Policy, module, qid string, correct bool, now time.Time) {
	card, ok := p.Review.Cards[qid]
	if !ok {
		box := 0
		if correct {
			box = 1
		}
		p.Review.Cards[qid] = &progress.Card{Module: module, Box: box, Due: progress.AddDays(now, pol.ReviewIntervalsDays[box])}
		return
	}
	if !correct {
		card.Box = 0
		card.Due = progress.AddDays(now, pol.ReviewIntervalsDays[0])
	}
}

// Due returns the ids of cards due today (or earlier) whose questions still exist,
// most overdue first, interleaving modules, at most limit.
func Due(p *progress.Progress, idx map[string]*course.Question, now time.Time, limit int, rng *rand.Rand) []string {
	today := progress.Date(now)
	type due struct{ id, date, module string }
	var all []due
	for id, c := range p.Review.Cards {
		if c.Due <= today && idx[id] != nil {
			all = append(all, due{id, c.Due, c.Module})
		}
	}
	rng.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	sort.SliceStable(all, func(i, j int) bool { return all[i].date < all[j].date })
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	// Interleave: avoid two consecutive cards from the same module when possible.
	out := make([]string, 0, len(all))
	for len(all) > 0 {
		pick := 0
		if len(out) > 0 {
			prev := p.Review.Cards[out[len(out)-1]].Module
			for i, d := range all {
				if d.module != prev {
					pick = i
					break
				}
			}
		}
		out = append(out, all[pick].id)
		all = append(all[:pick], all[pick+1:]...)
	}
	return out
}

// Answer applies a review result to a card and appends it to the review log.
func Answer(p *progress.Progress, pol course.Policy, qid string, correct bool, now time.Time) {
	card, ok := p.Review.Cards[qid]
	if !ok {
		return
	}
	t := now
	card.LastSeen = &t
	card.Reps++
	if correct {
		if card.Box < len(pol.ReviewIntervalsDays)-1 {
			card.Box++
		}
	} else {
		card.Box = 0
		card.Lapses++
	}
	card.Due = progress.AddDays(now, pol.ReviewIntervalsDays[card.Box])
	p.Review.Log = append(p.Review.Log, progress.ReviewEntry{At: now, QID: qid, Module: card.Module, Correct: correct})
}

// Recap picks up to n questions from the modules most recently worked on, regardless
// of due date, for a warm-up after a long gap.
func Recap(p *progress.Progress, idx map[string]*course.Question, n int, rng *rand.Rand) []string {
	recent := map[string]bool{}
	var order []string
	for i := len(p.Activity) - 1; i >= 0 && len(order) < 3; i-- {
		m := p.Activity[i].Module
		if m != "" && !recent[m] {
			recent[m] = true
			order = append(order, m)
		}
	}
	var ids []string
	for id, c := range p.Review.Cards {
		if recent[c.Module] && idx[id] != nil {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	rng.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	if len(ids) > n {
		ids = ids[:n]
	}
	return ids
}
