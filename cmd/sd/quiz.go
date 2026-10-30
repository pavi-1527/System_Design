package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/chethu007/system-design-course/internal/course"
	"github.com/chethu007/system-design-course/internal/mastery"
	"github.com/chethu007/system-design-course/internal/progress"
	"github.com/chethu007/system-design-course/internal/quiz"
	"github.com/chethu007/system-design-course/internal/review"
)

// askItem presents one item and returns its graded result. ok=false means quit/EOF.
func (a *app) askItem(i, total int, it quiz.Item) (quiz.Result, bool) {
	q := it.Q
	kind := map[string]string{course.QSingle: "pick one", course.QMulti: "select ALL that apply, e.g. 1,3", course.QNumeric: "number"}[q.Type]
	a.printf("\n── Q%d/%d  [%s]  %s  (%s)\n", i, total, q.ID, strings.Join(q.Tags, ", "), kind)
	a.printf("%s\n", wrap(q.Prompt, 96, ""))
	if q.Type == course.QNumeric {
		if q.Unit != "" {
			a.printf("  (answer in %s; suffixes like 1.2k, 3M, 2e6 and units like 40TB work)\n", q.Unit)
		}
	} else {
		for d, ch := range it.Choices() {
			a.printf("%s\n", wrap(fmt.Sprintf("%d) %s", d+1, ch), 92, "  "))
		}
	}
	for {
		in, ok := a.ask("> ")
		if !ok || in == "q" || in == "quit" {
			return quiz.Result{}, false
		}
		if in == "s" || in == "skip" || in == "?" {
			res := it.Skip()
			a.showResult(res)
			return res, true
		}
		res, err := it.Grade(in)
		if err != nil {
			a.printf("  %v — try again (s = skip, q = quit without recording)\n", err)
			continue
		}
		a.showResult(res)
		return res, true
	}
}

func (a *app) showResult(r quiz.Result) {
	if r.Correct {
		a.printf("  ✓ Correct.")
	} else {
		a.printf("  ✗ Not quite. You: %s\n    Expected: %s", r.Given, r.Expected)
	}
	if r.Note != "" {
		a.printf("  (%s)", r.Note)
	}
	a.printf("\n%s\n", wrap("Why: "+r.Explanation, 92, "    "))
	if r.Ref != "" {
		a.printf("    See: %s\n", r.Ref)
	}
}

func (a *app) cmdQuiz(args []string) error {
	fs := newFlags("quiz")
	n := fs.Int("n", 0, "number of questions (default: the module's draw)")
	pos, err := splitArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: sd quiz MODULE [-n N]")
	}
	m, err := a.c.Module(pos[0])
	if err != nil {
		return err
	}
	if ok, missing := mastery.Unlocked(a.c, m, a.p); !ok {
		a.printf("Note: %s is locked (needs %s). You can practise, but the tutor will steer you to the prerequisites first.\n", m.ID, strings.Join(missing, ", "))
	}
	qz, err := a.c.LoadQuiz(m)
	if err != nil {
		return err
	}
	full := quiz.FullLength(qz, a.c.Policy.DefaultQuizDraw)
	draw := full
	if *n > 0 {
		draw = *n
	}
	// Only full-length attempts count towards the gate; shorter ones are practice.
	gated := draw >= full
	items := quiz.Draw(qz, draw, quiz.Seen(a.p.Peek(m.ID)), a.rng)
	a.printf("%s quiz — %s · %d questions · pass mark %s", m.ID, m.Title, len(items), pct(a.c.Policy.QuizPassThreshold))
	if !gated {
		a.printf(" · SHORT PRACTICE (fewer questions than the gate draw; still recorded for review, not for the gate)")
	}
	a.printf("\nAnswer with the choice number, \"1,3\" for multi-select, or a quantity for numeric questions.\n")
	a.printf("Commit to an answer before you look anything up — that's the point. s = skip, q = quit (nothing recorded).\n")

	var answers []progress.AnswerRecord
	correct := 0
	tagMiss := map[string]int{}
	for i, it := range items {
		res, ok := a.askItem(i+1, len(items), it)
		if !ok {
			return errAborted
		}
		answers = append(answers, progress.AnswerRecord{QID: it.Q.ID, Correct: res.Correct, Given: res.Given})
		if res.Correct {
			correct++
		} else {
			for _, t := range it.Q.Tags {
				tagMiss[t]++
			}
		}
	}
	score, passed := mastery.CompleteQuiz(a.c, a.p, m.ID, answers, gated, a.now)
	a.printf("\n%s: %d/%d = %s ", m.ID, correct, len(items), pct(score))
	switch {
	case !gated:
		a.printf("(practice)\n")
	case score+1e-9 >= a.c.Policy.QuizPassThreshold:
		a.printf("— PASS.\n")
	default:
		a.printf("— below the %s pass mark.\n", pct(a.c.Policy.QuizPassThreshold))
	}
	if len(tagMiss) > 0 {
		type tc struct {
			t string
			n int
		}
		var tcs []tc
		for t, n := range tagMiss {
			tcs = append(tcs, tc{t, n})
		}
		sort.Slice(tcs, func(i, j int) bool { return tcs[i].n > tcs[j].n || (tcs[i].n == tcs[j].n && tcs[i].t < tcs[j].t) })
		var parts []string
		for _, x := range tcs {
			parts = append(parts, fmt.Sprintf("%s ×%d", x.t, x.n))
		}
		a.printf("Missed by tag: %s. Re-read the referenced sections; missed questions come back in `sd review` tomorrow.\n", strings.Join(parts, ", "))
	}
	for _, c := range mastery.Gate(a.c, m, a.p) {
		if c.Name == course.GateQuiz {
			a.printf("Gate (quiz): %s\n", c.Detail)
		}
	}
	a.announcePassed(passed)
	return a.save()
}

func (a *app) cmdReview(args []string) error {
	fs := newFlags("review")
	n := fs.Int("n", a.c.Policy.ReviewSessionSize, "maximum cards this session")
	if _, err := splitArgs(fs, args); err != nil {
		return err
	}
	idx := a.c.QuestionIndex()
	due := review.Due(a.p, idx, a.now, *n, a.rng)
	if len(due) == 0 {
		r := mastery.Evaluate(a.c, a.p, a.now)
		if r.NextReviewDue != "" {
			a.printf("Nothing due today. Next card is due %s.\n", r.NextReviewDue)
		} else {
			a.printf("Your review deck is empty. Questions join it when you take `sd quiz`.\n")
		}
		return nil
	}
	a.printf("Spaced review — %d cards due. Misses reset to tomorrow; hits move to a longer interval.\n", len(due))
	correct := 0
	done := 0
	for i, qid := range due {
		it := quiz.Prepare(idx[qid], a.rng)
		res, ok := a.askItem(i+1, len(due), it)
		if !ok {
			break // answers so far are kept: each review card stands alone
		}
		review.Answer(a.p, a.c.Policy, qid, res.Correct, a.now)
		done++
		if res.Correct {
			correct++
		}
	}
	if done == 0 {
		return errAborted
	}
	a.p.Touch(a.now, "review", "", fmt.Sprintf("%d/%d", correct, done))
	a.printf("\nReview: %d/%d correct (%s).\n", correct, done, pct(quiz.Score(correct, done)))
	for _, id := range mastery.CheckRefresh(a.c, a.p, a.now) {
		m, _ := a.c.Module(id)
		a.printf("!! %s (%s) is now marked NEEDS REFRESH: %s. Revisit it before new material; `sd quiz %s` ≥ %s clears it.\n",
			id, m.Title, a.p.Peek(id).RefreshReason, id, pct(a.c.Policy.QuizPassThreshold))
	}
	return a.save()
}

func (a *app) cmdRecap(args []string) error {
	fs := newFlags("recap")
	asJSON := fs.Bool("json", false, "machine-readable output")
	noQuiz := fs.Bool("no-quiz", false, "summary only, no warm-up questions")
	if _, err := splitArgs(fs, args); err != nil {
		return err
	}
	r := mastery.Evaluate(a.c, a.p, a.now)
	idx := a.c.QuestionIndex()
	weak := mastery.WeakTags(a.p, idx, 20, 3)
	var recent []progress.Activity
	if n := len(a.p.Activity); n > 0 {
		recent = a.p.Activity[max(0, n-10):]
	}
	var inProgress []mastery.ModuleStatus
	for _, st := range r.Modules {
		if st.State == mastery.InProgress || st.State == mastery.NeedsRefresh {
			inProgress = append(inProgress, st)
		}
	}
	if *asJSON {
		return a.json(struct {
			GapDays    int                    `json:"gap_days"`
			RecapDue   bool                   `json:"recap_due"`
			Recent     []progress.Activity    `json:"recent_activity"`
			InProgress []mastery.ModuleStatus `json:"in_progress"`
			WeakTags   []mastery.TagStat      `json:"weak_tags"`
			Next       []string               `json:"next"`
		}{r.GapDays, r.RecapDue, recent, inProgress, weak, r.Next})
	}
	if a.p.LastActiveAt == nil {
		a.printf("Nothing to recap yet — no activity recorded.\n")
		return nil
	}
	a.printf("Recap — last active %d day(s) ago (%s).\n", r.GapDays, progress.Date(*a.p.LastActiveAt))
	a.printf("\nWhat you were doing:\n")
	for _, e := range recent {
		a.printf("  %s  %-14s %-4s %s\n", e.At.Local().Format("2006-01-02"), e.Kind, e.Module, e.Detail)
	}
	if len(inProgress) > 0 {
		a.printf("\nOpen modules:\n")
		for _, st := range inProgress {
			a.printf("  %s %s — %s\n", st.ID, st.Title, stateLabel(st))
		}
	}
	if len(weak) > 0 {
		a.printf("\nWeakest tags:")
		for i, w := range weak {
			if i == 5 {
				break
			}
			a.printf(" %s %s;", w.Tag, pct(w.Accuracy))
		}
		a.printf("\n")
	}
	if *noQuiz {
		return nil
	}
	ids := review.Recap(a.p, idx, 5, a.rng)
	if len(ids) == 0 {
		return nil
	}
	a.printf("\nWarm-up: %d questions from what you last studied. Misses go back into tomorrow's review.\n", len(ids))
	correct, done := 0, 0
	for i, qid := range ids {
		res, ok := a.askItem(i+1, len(ids), quiz.Prepare(idx[qid], a.rng))
		if !ok {
			break
		}
		done++
		if res.Correct {
			correct++
		} else {
			review.Answer(a.p, a.c.Policy, qid, false, a.now)
		}
	}
	if done == 0 {
		return nil
	}
	a.p.Touch(a.now, "recap", "", fmt.Sprintf("%d/%d", correct, done))
	a.printf("\nWarm-up: %d/%d. Now ask the tutor (TUTOR.md) for a recap conversation, then `sd review`.\n", correct, done)
	return a.save()
}
