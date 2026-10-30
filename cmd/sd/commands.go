package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/chethu007/system-design-course/internal/course"
	"github.com/chethu007/system-design-course/internal/estimate"
	"github.com/chethu007/system-design-course/internal/labrun"
	"github.com/chethu007/system-design-course/internal/mastery"
	"github.com/chethu007/system-design-course/internal/progress"
)

func (a *app) cmdEstimate(args []string) error {
	fs := newFlags("estimate")
	n := fs.Int("n", 5, "number of problems")
	kind := fs.String("kind", "any", "problem kind: any, "+strings.Join(estimate.Kinds(), ", "))
	seed := fs.Uint64("seed", 0, "random seed (0 = random) — reuse to replay a set")
	tol := fs.Float64("tol", a.c.Policy.EstimateTolerance, "relative tolerance; only the default tolerance counts towards mastery")
	if _, err := splitArgs(fs, args); err != nil {
		return err
	}
	rng := a.rng
	if *seed != 0 {
		rng = rand.New(rand.NewPCG(*seed, 0x5d5d))
	}
	counted := *tol == a.c.Policy.EstimateTolerance
	a.printf("Estimation drill — %d problem(s), accepting ±%s. Work it on paper first; units matter.\n", *n, pct(*tol))
	a.printf("Answers like 1.2k, 3.5M, 2e6, 40TB, 12 Gbps, 45 min are understood. s = show solution, q = quit.\n")
	within := 0
	done := 0
	for i := 0; i < *n; i++ {
		p, err := estimate.Generate(*kind, rng)
		if err != nil {
			return err
		}
		a.printf("\n── Problem %d/%d [%s]\n%s\n  (answer in %s)\n", i+1, *n, p.Kind, wrap(p.Prompt, 96, ""), p.Unit)
		var res estimate.Result
		gave := false
		for {
			in, ok := a.ask("> ")
			if !ok || in == "q" {
				if done > 0 {
					return a.finishEstimate(within, done)
				}
				return errAborted
			}
			if in == "s" || in == "skip" {
				break
			}
			r, err := estimate.Check(p, in, *tol)
			if err != nil {
				a.printf("  %v — try again\n", err)
				continue
			}
			res, gave = r, true
			break
		}
		done++
		if gave {
			verdict := "✗ outside tolerance"
			if res.Within {
				verdict = "✓ within tolerance"
				within++
			}
			a.printf("  %s — you: %s, reference: %s (ratio %.2f×)", verdict, estimate.Show(res.Given, p.Unit), res.Display, res.Ratio)
			if res.Note != "" {
				a.printf(" (%s)", res.Note)
			}
			a.printf("\n")
			if !res.Within && res.Ratio > 0 {
				a.printf("%s\n", wrap(estimate.Diagnose(res.Ratio), 92, "  "))
			}
		} else {
			a.printf("  Skipped (counts as a miss). Reference: %s\n", estimate.Show(p.Answer, p.Unit))
		}
		a.printf("  Worked solution:\n")
		for _, s := range p.Steps {
			a.printf("%s\n", wrap("• "+s, 92, "    "))
		}
		a.printf("%s\n", wrap("So what: "+p.Decision, 92, "  "))
		if counted {
			a.p.Estimate.Attempts = append(a.p.Estimate.Attempts, progress.EstimateAttempt{
				At: a.now, Kind: p.Kind, Expected: p.Answer, Given: res.Given, Ratio: res.Ratio, Within: gave && res.Within,
			})
		}
	}
	return a.finishEstimate(within, done)
}

func (a *app) finishEstimate(within, done int) error {
	a.p.Touch(a.now, "estimate", "", fmt.Sprintf("%d/%d within tolerance", within, done))
	es := mastery.EstimateSummary(a.c.Policy, a.p)
	a.printf("\nThis set: %d/%d within tolerance. Rolling: %s over the last %d (mastery = ≥ %s over %d, all kinds).\n",
		within, done, pct(es.WindowRate), es.Window, pct(a.c.Policy.EstimateMasteryRate), a.c.Policy.EstimateMasteryWindow)
	if es.Mastered {
		a.printf("Estimation: MASTERED at the current window. Keep it warm with a few problems a week.\n")
	} else if len(es.KindsMissing) > 0 {
		a.printf("Not yet attempted: %s (use -kind).\n", strings.Join(es.KindsMissing, ", "))
	}
	return a.save()
}

func (a *app) cmdLab(args []string) error {
	fs := newFlags("lab")
	all := fs.Bool("all", false, "also run STRETCH lab packages (never gating)")
	noRace := fs.Bool("no-race", false, "skip the race detector")
	verbose := fs.Bool("v", false, "show all test output")
	pos, err := splitArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: sd lab MODULE [--all] [--no-race] [-v]")
	}
	m, err := a.c.Module(pos[0])
	if err != nil {
		return err
	}
	if !m.Written() {
		return fmt.Errorf("%s is planned but not written yet", m.ID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	race := !*noRace && labrun.RaceAvailable(ctx)
	if !*noRace && !race {
		a.printf("Note: cgo is disabled, so the race detector is unavailable; running without -race.\n")
	}
	a.printf("Running %s labs (%s)%s ...\n", m.ID, m.Paths.Lab, map[bool]string{true: " with -race", false: ""}[race])
	run, err := labrun.Run(ctx, a.c.Root, m, labrun.Options{IncludeStretch: *all, Race: race, Timeout: 5 * time.Minute, Verbose: *verbose}, a.out)
	if err != nil {
		return err
	}
	run.At = a.now
	a.printf("\n")
	for _, pk := range run.Packages {
		mark := "✗"
		if pk.Passed {
			mark = "✓"
		}
		extra := ""
		if pk.Failed > 0 {
			extra = fmt.Sprintf(" (%d failing tests)", pk.Failed)
		}
		a.printf("  %s %-14s %s%s\n", mark, pk.Package, coreWord(pk.Core), extra)
	}
	var passed []string
	if *noRace && labrun.RaceAvailable(ctx) {
		a.printf("Run without -race: not recorded for the gate (the gate wants the race detector on).\n")
	} else {
		passed = mastery.RecordLab(a.c, a.p, m.ID, run)
	}
	if run.Passed {
		a.printf("All core labs pass.\n")
	} else {
		a.printf("Core labs not passing yet. Read the failure messages: they say what behaviour is expected and why.\n")
	}
	a.announcePassed(passed)
	return a.save()
}

func (a *app) cmdRecord(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: sd record design|teachback|mock|note MODULE [flags]")
	}
	what, rest := args[0], args[1:]
	fs := newFlags("record " + what)
	self := fs.String("self", "", "self-score (below|approaching|meets|exceeds)")
	tutor := fs.String("tutor", "", "tutor score (below|approaching|meets|exceeds)")
	result := fs.String("result", "", "teach-back verdict (pass|revise)")
	notes := fs.String("notes", "", "short notes (what was missed, what to work on)")
	exercise := fs.String("exercise", "", "exercise name (defaults to the module's design exercise)")
	format := fs.String("format", "", "mock format: "+strings.Join(course.MockFormats, ", "))
	problem := fs.String("problem", "", "mock problem name")
	minutes := fs.Int("minutes", 0, "mock duration in minutes")
	level := fs.String("level", "", "mock overall level (below|approaching|meets|exceeds)")
	tags := fs.String("tags", "", "comma-separated tags for a note")
	pos, err := splitArgs(fs, rest)
	if err != nil {
		return err
	}
	var m *course.Module
	if len(pos) > 0 {
		if m, err = a.c.Module(pos[0]); err != nil {
			return err
		}
	} else if what != "note" {
		return fmt.Errorf("usage: sd record %s MODULE [flags]", what)
	}
	var passed []string
	switch what {
	case "design":
		if !m.Requires(course.GateDesign) {
			return fmt.Errorf("%s has no design component in its gate", m.ID)
		}
		ex := *exercise
		if ex == "" {
			ex = m.Paths.DesignExercise
		}
		passed, err = mastery.RecordDesign(a.c, a.p, m.ID, progress.Assessment{At: a.now, Exercise: ex, Self: *self, Tutor: *tutor, Notes: *notes})
	case "teachback":
		if !m.Requires(course.GateTeachback) {
			return fmt.Errorf("%s has no teach-back component in its gate", m.ID)
		}
		passed, err = mastery.RecordTeachback(a.c, a.p, m.ID, progress.Assessment{At: a.now, Result: *result, Notes: *notes})
	case "mock":
		passed, err = mastery.RecordMock(a.c, a.p, m.ID, progress.Mock{At: a.now, Format: *format, Problem: *problem, Minutes: *minutes, Level: *level, Notes: *notes})
	case "note":
		if *notes == "" {
			return fmt.Errorf("note needs --notes")
		}
		id := ""
		if m != nil {
			id = m.ID
		}
		var tl []string
		for _, t := range strings.Split(*tags, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tl = append(tl, t)
			}
		}
		a.p.TutorNotes = append(a.p.TutorNotes, progress.TutorNote{At: a.now, Module: id, Tags: tl, Note: *notes})
		a.p.Touch(a.now, "note", id, "")
	default:
		return fmt.Errorf("unknown record type %q (design, teachback, mock, note)", what)
	}
	if err != nil {
		return err
	}
	a.printf("Recorded %s", what)
	if m != nil {
		a.printf(" for %s", m.ID)
		a.printf(".\n")
		for _, c := range mastery.Gate(a.c, m, a.p) {
			mark := "[ ]"
			if c.Met {
				mark = "[x]"
			}
			a.printf("  %s %-10s %s\n", mark, c.Name, c.Detail)
		}
	} else {
		a.printf(".\n")
	}
	a.announcePassed(passed)
	return a.save()
}

func (a *app) cmdValidate(args []string) error {
	fs := newFlags("validate")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if _, err := splitArgs(fs, args); err != nil {
		return err
	}
	issues := course.Validate(a.c)
	if *asJSON {
		if issues == nil {
			issues = []course.Issue{}
		}
		if err := a.json(issues); err != nil {
			return err
		}
	} else {
		for _, is := range issues {
			a.printf("%s\n", is)
		}
	}
	errs := 0
	for _, is := range issues {
		if is.Severity == "error" {
			errs++
		}
	}
	written := 0
	for _, m := range a.c.Modules {
		if m.Written() {
			written++
		}
	}
	if !*asJSON {
		a.printf("%d modules (%d written), %d errors, %d warnings.\n", len(a.c.Modules), written, errs, len(issues)-errs)
	}
	if errs > 0 {
		return fmt.Errorf("validation failed")
	}
	return nil
}
