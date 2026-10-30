package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/chethu007/system-design-course/internal/course"
	"github.com/chethu007/system-design-course/internal/mastery"
	"github.com/chethu007/system-design-course/internal/progress"
)

func (a *app) cmdStatus(args []string) error {
	fs := newFlags("status")
	asJSON := fs.Bool("json", false, "machine-readable output")
	pos, err := splitArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return a.moduleStatus(pos[0], *asJSON)
	}
	r := mastery.Evaluate(a.c, a.p, a.now)
	if *asJSON {
		return a.json(r)
	}
	a.printf("%s — status (%s)\n", a.c.Title, progress.Date(a.now))
	switch {
	case a.p.LastActiveAt == nil:
		a.printf("No activity yet. Start with README.md, then A0 and B0 (both are unlocked).\n")
	case r.GapDays == 0:
		a.printf("Last active: today.\n")
	default:
		a.printf("Last active: %d day(s) ago (%s).\n", r.GapDays, progress.Date(*a.p.LastActiveAt))
	}
	if r.RecapDue {
		a.printf("\n>>> You've been away more than %d days. Run `sd recap` and ask the tutor for a recap first. <<<\n", a.c.Policy.RecapGapDays)
	}
	a.printf("\nNext:\n")
	for i, n := range r.Next {
		a.printf("%s\n", wrap(fmt.Sprintf("%d. %s", i+1, n), 96, "  "))
	}

	tracks := map[string]string{}
	for _, t := range a.c.Tracks {
		tracks[t.ID] = t.Title
	}
	byTrack := map[string][]mastery.ModuleStatus{}
	for _, st := range r.Modules {
		byTrack[st.Track] = append(byTrack[st.Track], st)
	}
	for _, t := range a.c.Tracks {
		ms := byTrack[t.ID]
		sort.SliceStable(ms, func(i, j int) bool { return idLess(ms[i].ID, ms[j].ID) })
		a.printf("\nTrack %s — %s\n", t.ID, t.Title)
		for _, st := range ms {
			title := st.Title
			if r := []rune(title); len(r) > 44 {
				title = string(r[:43]) + "…"
			}
			core := ""
			if !st.Core {
				core = " (stretch)"
			}
			a.printf("  %-4s %-45s %4.0fh  %s%s\n", st.ID, title, st.Hours, stateLabel(st), core)
		}
	}
	a.printf("\nReviews: %d due", r.DueReviews)
	if r.NextReviewDue != "" {
		a.printf(" (next scheduled %s)", r.NextReviewDue)
	}
	a.printf(".\n")
	es := r.Estimate
	a.printf("Estimation: %d/%d recent drills, %s within ±%s", es.Window, a.c.Policy.EstimateMasteryWindow, pct(es.WindowRate), pct(a.c.Policy.EstimateTolerance))
	if es.Mastered {
		a.printf(" — MASTERED")
	} else if len(es.KindsMissing) > 0 {
		a.printf("; untried kinds: %s", strings.Join(es.KindsMissing, ", "))
	}
	a.printf(".\n")
	a.printf("Core hours behind passed gates: %.0f / %.0f.\n", r.HoursPassed, r.HoursTotal)
	return nil
}

// idLess orders A2 before A10.
func idLess(x, y string) bool {
	if x[0] != y[0] {
		return x[0] < y[0]
	}
	if len(x) != len(y) {
		return len(x) < len(y)
	}
	return x < y
}

func stateLabel(st mastery.ModuleStatus) string {
	switch st.State {
	case mastery.Locked:
		s := "locked (needs " + strings.Join(st.MissingPrereqs, ", ") + ")"
		if !st.Written {
			s = "planned · " + s
		}
		return s
	case mastery.Planned:
		return "planned — unlocked, not written yet"
	case mastery.Available:
		return "available"
	case mastery.InProgress:
		return "in progress  " + componentLine(st.Components)
	case mastery.Passed:
		s := "PASSED " + progress.Date(*st.GatePassedAt)
		if st.DueCards > 0 {
			s += fmt.Sprintf(" · %d reviews due", st.DueCards)
		}
		if st.ReviewOverdue {
			s += " · review overdue"
		}
		return s
	case mastery.NeedsRefresh:
		return "NEEDS REFRESH (" + st.RefreshReason + ")"
	}
	return string(st.State)
}

func componentLine(cs []mastery.Component) string {
	var parts []string
	for _, c := range cs {
		mark := "·"
		if c.Met {
			mark = "✓"
		}
		parts = append(parts, c.Name+" "+mark)
	}
	return "[" + strings.Join(parts, "  ") + "]"
}

func (a *app) moduleStatus(id string, asJSON bool) error {
	m, err := a.c.Module(id)
	if err != nil {
		return err
	}
	st := mastery.Status(a.c, m, a.p, a.now)
	if asJSON {
		return a.json(struct {
			Module *course.Module       `json:"module"`
			Status mastery.ModuleStatus `json:"status"`
		}{m, st})
	}
	a.printf("%s — %s\n", m.ID, m.Title)
	a.printf("%s\n", wrap(m.Summary, 96, "  "))
	a.printf("Track %s · %.0fh (core %.0f, stretch %.0f) · %s · prerequisites: %s\n",
		m.Track, m.TotalHours(), m.Hours.Core, m.Hours.Stretch, coreWord(m.Core), orNone(m.Prerequisites))
	a.printf("State: %s\n", stateLabel(st))
	if m.Written() {
		a.printf("Start here: %s\n", m.Paths.Readme)
	}
	a.printf("\nMastery gate:\n")
	for _, c := range st.Components {
		mark := "[ ]"
		if c.Met {
			mark = "[x]"
		}
		a.printf("  %s %-10s %s\n", mark, c.Name, c.Detail)
		if !c.Met && c.Action != "" {
			a.printf("      → %s\n", c.Action)
		}
	}
	if len(m.Units) > 0 {
		a.printf("\nUnits:\n")
		for _, u := range m.Units {
			a.printf("  %d. %-50s %3.0fh core + %.0fh stretch  %s\n", u.N, u.Title, u.Hours.Core, u.Hours.Stretch, u.File)
		}
	}
	if len(m.Labs) > 0 {
		a.printf("\nLabs (%s):\n", m.Paths.Lab)
		for _, l := range m.Labs {
			a.printf("  %-14s %-8s %s\n", l.Package, coreWord(l.Core), l.Title)
		}
	}
	return nil
}

func coreWord(core bool) string {
	if core {
		return "CORE"
	}
	return "STRETCH"
}

func orNone(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, ", ")
}

func (a *app) cmdProgress(args []string) error {
	fs := newFlags("progress")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if _, err := splitArgs(fs, args); err != nil {
		return err
	}
	idx := a.c.QuestionIndex()
	weak := mastery.WeakTags(a.p, idx, 20, 3)
	if *asJSON {
		return a.json(struct {
			Report   *mastery.Report    `json:"report"`
			WeakTags []mastery.TagStat  `json:"weak_tags"`
			ByKind   map[string][2]int  `json:"estimate_by_kind"`
			Progress *progress.Progress `json:"progress"`
		}{mastery.Evaluate(a.c, a.p, a.now), weak, mastery.EstimateByKind(a.p), a.p})
	}
	a.printf("Progress detail (%s)\n", progress.Date(a.now))
	if a.p.Learner.StartedAt != nil {
		a.printf("Started %s · %d days in.\n", progress.Date(*a.p.Learner.StartedAt), progress.DaysBetween(*a.p.Learner.StartedAt, a.now))
	}
	any := false
	for _, m := range a.c.Ordered() {
		rec := a.p.Peek(m.ID)
		if !rec.HasActivity() {
			continue
		}
		any = true
		a.printf("\n%s — %s\n", m.ID, m.Title)
		for _, c := range mastery.Gate(a.c, m, a.p) {
			mark := "[ ]"
			if c.Met {
				mark = "[x]"
			}
			a.printf("  %s %-10s %s\n", mark, c.Name, c.Detail)
		}
		for _, at := range rec.QuizAttempts {
			a.printf("    quiz      %s  %d/%d  %s\n", at.At.Local().Format("2006-01-02 15:04"), at.Correct, at.Total, pct(at.Score))
		}
		for _, d := range rec.Designs {
			a.printf("    design    %s  self=%s tutor=%s  %s\n", progress.Date(d.At), dash(d.Self), dash(d.Tutor), d.Notes)
		}
		for _, t := range rec.Teachbacks {
			a.printf("    teachback %s  %s  %s\n", progress.Date(t.At), t.Result, t.Notes)
		}
		for _, mk := range rec.Mocks {
			a.printf("    mock      %s  %s %q %dmin %s  %s\n", progress.Date(mk.At), mk.Format, mk.Problem, mk.Minutes, mk.Level, mk.Notes)
		}
		if rec.NeedsRefresh {
			a.printf("    NEEDS REFRESH: %s\n", rec.RefreshReason)
		}
	}
	if !any {
		a.printf("\nNo module activity yet.\n")
	}
	if len(weak) > 0 {
		a.printf("\nWeakest tags (recent answers):\n")
		for i, w := range weak {
			if i == 8 {
				break
			}
			a.printf("  %-22s %3s  (%d/%d)\n", w.Tag, pct(w.Accuracy), w.Correct, w.Total)
		}
	}
	if bk := mastery.EstimateByKind(a.p); len(bk) > 0 {
		a.printf("\nEstimation by kind (all time):\n")
		for _, k := range mastery.EstimateKinds {
			v := bk[k]
			if v[1] == 0 {
				a.printf("  %-13s —\n", k)
				continue
			}
			a.printf("  %-13s %3s  (%d/%d)\n", k, pct(float64(v[0])/float64(v[1])), v[0], v[1])
		}
	}
	if n := len(a.p.Review.Cards); n > 0 {
		boxes := make([]int, len(a.c.Policy.ReviewIntervalsDays))
		lapses := 0
		for _, c := range a.p.Review.Cards {
			if c.Box < len(boxes) {
				boxes[c.Box]++
			}
			lapses += c.Lapses
		}
		a.printf("\nReview deck: %d cards; by box (interval days %v): %v; total lapses %d.\n", n, a.c.Policy.ReviewIntervalsDays, boxes, lapses)
	}
	if n := len(a.p.TutorNotes); n > 0 {
		a.printf("\nRecent tutor notes:\n")
		start := max(0, n-8)
		for _, t := range a.p.TutorNotes[start:] {
			a.printf("  %s %-4s [%s] %s\n", progress.Date(t.At), t.Module, strings.Join(t.Tags, ","), t.Note)
		}
	}
	return nil
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
