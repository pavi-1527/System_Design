// Package mastery evaluates mastery gates, module states (locked/available/passed/
// needs refresh), and what the learner should do next. It is pure logic over
// course.Course and progress.Progress, so the CLI and any UI share one source of truth.
package mastery

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chethu007/system-design-course/internal/course"
	"github.com/chethu007/system-design-course/internal/progress"
	"github.com/chethu007/system-design-course/internal/review"
)

// State of a module for the learner.
type State string

const (
	Planned      State = "planned"       // content not written yet
	Locked       State = "locked"        // prerequisites' gates not passed
	Available    State = "available"     // unlocked, nothing recorded yet
	InProgress   State = "in_progress"   // unlocked, some gate work recorded
	Passed       State = "passed"        // gate passed
	NeedsRefresh State = "needs_refresh" // gate passed earlier, review scores dropped
)

// Component is the status of one gate requirement.
type Component struct {
	Name   string `json:"name"`
	Met    bool   `json:"met"`
	Detail string `json:"detail"`
	Action string `json:"action,omitempty"`
}

// ModuleStatus is the evaluated state of one module.
type ModuleStatus struct {
	ID             string      `json:"id"`
	Title          string      `json:"title"`
	Track          string      `json:"track"`
	Core           bool        `json:"core"`
	Written        bool        `json:"written"`
	Hours          float64     `json:"hours"`
	State          State       `json:"state"`
	MissingPrereqs []string    `json:"missing_prereqs,omitempty"`
	Components     []Component `json:"components"`
	GateMet        bool        `json:"gate_met"`
	GatePassedAt   *time.Time  `json:"gate_passed_at,omitempty"`
	RefreshReason  string      `json:"refresh_reason,omitempty"`
	ReviewOverdue  bool        `json:"review_overdue"`
	DueCards       int         `json:"due_cards"`
}

// EstimateStatus summarises the estimation drills.
type EstimateStatus struct {
	Attempts     int      `json:"attempts"`
	Window       int      `json:"window"`
	WindowRate   float64  `json:"window_rate"`
	KindsCovered []string `json:"kinds_covered"`
	KindsMissing []string `json:"kinds_missing"`
	Mastered     bool     `json:"mastered"`
}

// Report is the full evaluated picture used by `sd status` / `sd progress`.
type Report struct {
	Now           time.Time      `json:"now"`
	GapDays       int            `json:"gap_days"`
	RecapDue      bool           `json:"recap_due"`
	Modules       []ModuleStatus `json:"modules"`
	DueReviews    int            `json:"due_reviews"`
	NextReviewDue string         `json:"next_review_due,omitempty"`
	Estimate      EstimateStatus `json:"estimate"`
	Next          []string       `json:"next"`
	HoursPassed   float64        `json:"hours_passed"`
	HoursTotal    float64        `json:"hours_total"`
}

// EstimateKinds are the problem kinds `sd estimate` generates; mastery requires all.
var EstimateKinds = []string{"qps", "storage", "bandwidth", "servers", "cache", "concurrency", "availability"}

// Gate evaluates one module's gate components without consulting prerequisites.
func Gate(c *course.Course, m *course.Module, p *progress.Progress) []Component {
	pol := c.Policy
	rec := p.Peek(m.ID)
	if rec == nil {
		rec = &progress.ModuleRecord{}
	}
	var out []Component
	for _, req := range m.Gate.Requires {
		switch req {
		case course.GateQuiz:
			out = append(out, quizComponent(pol, m, rec))
		case course.GateLab:
			out = append(out, labComponent(m, rec))
		case course.GateDesign:
			out = append(out, designComponent(pol, m, rec))
		case course.GateTeachback:
			out = append(out, teachbackComponent(m, rec))
		case course.GateMocks:
			out = append(out, mocksComponent(pol, m, rec))
		default:
			out = append(out, Component{Name: req, Detail: "unknown gate component (fix module.json)"})
		}
	}
	return out
}

func quizComponent(pol course.Policy, m *course.Module, rec *progress.ModuleRecord) Component {
	days := map[string]bool{}
	passes := 0
	best := 0.0
	for _, a := range rec.QuizAttempts {
		if a.Score > best {
			best = a.Score
		}
		if a.Score+1e-9 >= pol.QuizPassThreshold {
			passes++
			days[progress.Date(a.At)] = true
		}
	}
	got := passes
	if pol.QuizPassesDistinctDays {
		got = len(days)
	}
	need := pol.QuizPassesRequired
	c := Component{Name: course.GateQuiz, Met: got >= need}
	c.Detail = fmt.Sprintf("%d/%d passing attempts ≥ %.0f%%", min(got, need), need, pol.QuizPassThreshold*100)
	if pol.QuizPassesDistinctDays {
		c.Detail += " on different days"
	}
	if len(rec.QuizAttempts) > 0 {
		c.Detail += fmt.Sprintf(" (%d attempts, best %.0f%%)", len(rec.QuizAttempts), best*100)
	}
	if !c.Met {
		c.Action = "sd quiz " + m.ID
	}
	return c
}

func labComponent(m *course.Module, rec *progress.ModuleRecord) Component {
	c := Component{Name: course.GateLab}
	if len(m.CoreLabs()) == 0 {
		c.Detail = "no core labs listed in module.json"
		return c
	}
	if len(rec.LabRuns) == 0 {
		c.Detail = fmt.Sprintf("not run yet (%d core packages)", len(m.CoreLabs()))
		c.Action = "sd lab " + m.ID
		return c
	}
	last := rec.LabRuns[len(rec.LabRuns)-1]
	passed := 0
	core := 0
	for _, pk := range last.Packages {
		if pk.Core {
			core++
			if pk.Passed {
				passed++
			}
		}
	}
	c.Met = last.Passed
	c.Detail = fmt.Sprintf("last run %s: %d/%d core packages pass", progress.Date(last.At), passed, core)
	if !last.Race {
		c.Detail += " (without -race)"
	}
	if !c.Met {
		c.Action = "sd lab " + m.ID
	}
	return c
}

func designComponent(pol course.Policy, m *course.Module, rec *progress.ModuleRecord) Component {
	c := Component{Name: course.GateDesign}
	if len(rec.Designs) == 0 {
		c.Detail = "not attempted"
		c.Action = fmt.Sprintf("do %s, self-score it, then get the tutor's critique", m.Paths.DesignExercise)
		return c
	}
	last := rec.Designs[len(rec.Designs)-1]
	c.Met = last.Self != "" && pol.LevelRank(last.Tutor) >= pol.LevelRank(pol.DesignMinLevel)
	c.Detail = fmt.Sprintf("latest %s: self=%s tutor=%s (need tutor ≥ %s)", progress.Date(last.At), orDash(last.Self), orDash(last.Tutor), pol.DesignMinLevel)
	if !c.Met {
		c.Action = "revise the design using the tutor's critique and resubmit"
	}
	return c
}

func teachbackComponent(m *course.Module, rec *progress.ModuleRecord) Component {
	c := Component{Name: course.GateTeachback}
	if len(rec.Teachbacks) == 0 {
		c.Detail = "not attempted"
		c.Action = fmt.Sprintf("write the teach-back in %s and send it to the tutor", m.Paths.Teachback)
		return c
	}
	last := rec.Teachbacks[len(rec.Teachbacks)-1]
	c.Met = last.Result == "pass"
	c.Detail = fmt.Sprintf("latest %s: %s", progress.Date(last.At), last.Result)
	if last.Notes != "" {
		c.Detail += " — " + last.Notes
	}
	if !c.Met {
		c.Action = "rewrite the teach-back covering the missed items"
	}
	return c
}

func mocksComponent(pol course.Policy, m *course.Module, rec *progress.ModuleRecord) Component {
	need := m.Gate.MocksRequired
	if need == 0 {
		need = 1
	}
	good := len(countedMocks(pol, m, rec.Mocks))
	c := Component{Name: course.GateMocks, Met: good >= need}
	kind := ""
	if len(m.Gate.MockFormats) > 0 {
		kind = strings.Join(m.Gate.MockFormats, "/") + " "
	}
	distinct := ""
	if pol.MockDistinctProblems {
		distinct = " on distinct problems"
	}
	c.Detail = fmt.Sprintf("%d/%d %smocks at ≥ %s%s (%d run)", min(good, need), need, kind, pol.MockMinLevel, distinct, len(rec.Mocks))
	if !c.Met {
		c.Action = fmt.Sprintf("run a timed %smock with the tutor (see TUTOR.md, mock mode)", kind)
	}
	return c
}

// countedMocks returns the mocks that count towards m's gate: an accepted format, a
// level at or above the policy minimum and, when the policy asks for distinct problems,
// only the first such mock per problem.
func countedMocks(pol course.Policy, m *course.Module, mocks []progress.Mock) []progress.Mock {
	var out []progress.Mock
	seen := map[string]bool{}
	for _, mk := range mocks {
		if !m.AcceptsMockFormat(mk.Format) || pol.LevelRank(mk.Level) < pol.LevelRank(pol.MockMinLevel) {
			continue
		}
		if pol.MockDistinctProblems {
			k := ProblemKey(mk.Problem)
			if seen[k] {
				continue
			}
			seen[k] = true
		}
		out = append(out, mk)
	}
	return out
}

// ProblemKey normalises a mock problem name so that "News feed" and " news  FEED" are
// the same problem for the distinct-problems rule.
func ProblemKey(problem string) string {
	return strings.ToLower(strings.Join(strings.Fields(problem), " "))
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func allMet(cs []Component) bool {
	if len(cs) == 0 {
		return false
	}
	for _, c := range cs {
		if !c.Met {
			return false
		}
	}
	return true
}

// Reconcile stamps GatePassedAt on modules whose gate is now fully met, and returns
// the ids that passed just now. Call it after every mutation.
func Reconcile(c *course.Course, p *progress.Progress, now time.Time) []string {
	var passed []string
	for _, m := range c.Ordered() {
		rec := p.Peek(m.ID)
		if rec == nil || rec.GatePassedAt != nil {
			continue
		}
		if allMet(Gate(c, m, p)) {
			t := now
			rec.GatePassedAt = &t
			passed = append(passed, m.ID)
			p.Touch(now, "gate_passed", m.ID, "")
		}
	}
	return passed
}

// Unlocked reports whether every prerequisite's gate has passed; it returns the missing ones.
func Unlocked(c *course.Course, m *course.Module, p *progress.Progress) (bool, []string) {
	var missing []string
	for _, id := range m.Prerequisites {
		if r := p.Peek(id); r == nil || r.GatePassedAt == nil {
			missing = append(missing, id)
		}
	}
	return len(missing) == 0, missing
}

// Status evaluates one module.
func Status(c *course.Course, m *course.Module, p *progress.Progress, now time.Time) ModuleStatus {
	pol := c.Policy
	rec := p.Peek(m.ID)
	st := ModuleStatus{
		ID: m.ID, Title: m.Title, Track: m.Track, Core: m.Core,
		Written: m.Written(), Hours: m.TotalHours(),
		Components: Gate(c, m, p),
	}
	st.GateMet = allMet(st.Components)
	ok, missing := Unlocked(c, m, p)
	st.MissingPrereqs = missing
	today := progress.Date(now)
	for _, card := range p.Review.Cards {
		if card.Module == m.ID && card.Due <= today {
			st.DueCards++
		}
	}
	switch {
	case rec != nil && rec.GatePassedAt != nil:
		st.GatePassedAt = rec.GatePassedAt
		st.State = Passed
		if rec.NeedsRefresh {
			st.State = NeedsRefresh
			st.RefreshReason = rec.RefreshReason
		}
		last := *rec.GatePassedAt
		for _, e := range p.Review.Log {
			if e.Module == m.ID && e.At.After(last) {
				last = e.At
			}
		}
		st.ReviewOverdue = progress.DaysBetween(last, now) > pol.ReviewOverdueDays
	case !m.Written():
		st.State = Planned
		if !ok {
			st.State = Locked
		}
	case !ok:
		st.State = Locked
	case rec.HasActivity():
		st.State = InProgress
	default:
		st.State = Available
	}
	return st
}

// Evaluate builds the full report.
func Evaluate(c *course.Course, p *progress.Progress, now time.Time) *Report {
	pol := c.Policy
	r := &Report{Now: now, GapDays: p.GapDays(now)}
	r.RecapDue = p.LastActiveAt != nil && r.GapDays > pol.RecapGapDays
	for _, m := range c.Ordered() {
		st := Status(c, m, p, now)
		r.Modules = append(r.Modules, st)
		if m.Core {
			r.HoursTotal += st.Hours
			if st.GatePassedAt != nil {
				r.HoursPassed += st.Hours
			}
		}
	}
	today := progress.Date(now)
	for _, card := range p.Review.Cards {
		if card.Due <= today {
			r.DueReviews++
		} else if r.NextReviewDue == "" || card.Due < r.NextReviewDue {
			r.NextReviewDue = card.Due
		}
	}
	r.Estimate = EstimateSummary(pol, p)
	r.Next = recommend(c, r)
	return r
}

// EstimateSummary evaluates estimation-drill mastery over the policy window.
func EstimateSummary(pol course.Policy, p *progress.Progress) EstimateStatus {
	atts := p.Estimate.Attempts
	es := EstimateStatus{Attempts: len(atts)}
	win := atts
	if len(win) > pol.EstimateMasteryWindow {
		win = win[len(win)-pol.EstimateMasteryWindow:]
	}
	es.Window = len(win)
	kinds := map[string]bool{}
	ok := 0
	for _, a := range win {
		kinds[a.Kind] = true
		if a.Within {
			ok++
		}
	}
	if len(win) > 0 {
		es.WindowRate = float64(ok) / float64(len(win))
	}
	for _, k := range EstimateKinds {
		if kinds[k] {
			es.KindsCovered = append(es.KindsCovered, k)
		} else {
			es.KindsMissing = append(es.KindsMissing, k)
		}
	}
	es.Mastered = len(win) >= pol.EstimateMasteryWindow && es.WindowRate+1e-9 >= pol.EstimateMasteryRate && len(es.KindsMissing) == 0
	return es
}

func recommend(c *course.Course, r *Report) []string {
	var next []string
	if r.RecapDue {
		next = append(next, fmt.Sprintf("Welcome back — %d days since your last session. Run `sd recap`, then ask the tutor for a recap before new material.", r.GapDays))
	}
	for _, st := range r.Modules {
		if st.State == NeedsRefresh {
			next = append(next, fmt.Sprintf("Refresh %s (%s): review scores dropped (%s). Re-read the unit wrap-ups, then `sd quiz %s` ≥ 85%% to clear.", st.ID, st.Title, st.RefreshReason, st.ID))
		}
	}
	if r.DueReviews > 0 {
		next = append(next, fmt.Sprintf("`sd review` — %d cards due.", r.DueReviews))
	}
	// Work in progress first, then the next available modules; at most one per track
	// so tracks A and B interleave, and at most three in total.
	tracks := map[string]bool{}
	count := 0
	add := func(st ModuleStatus) {
		if tracks[st.Track] || count >= 3 {
			return
		}
		tracks[st.Track] = true
		count++
		if !st.Written {
			next = append(next, fmt.Sprintf("%s (%s) is next but not written yet — author it with MODULE_TEMPLATE.md in a new Claude session.", st.ID, st.Title))
			return
		}
		for _, comp := range st.Components {
			if !comp.Met {
				next = append(next, fmt.Sprintf("%s %s — %s: %s.", st.ID, st.Title, comp.Name, comp.Action))
				return
			}
		}
	}
	for _, st := range r.Modules {
		if st.State == InProgress {
			add(st)
		}
	}
	for _, st := range r.Modules {
		if st.State == Available || (st.State == Planned && len(st.MissingPrereqs) == 0) {
			add(st)
		}
	}
	a0 := findModule(r.Modules, "A0")
	if a0 != nil && a0.State != Locked && !r.Estimate.Mastered {
		msg := fmt.Sprintf("`sd estimate -n 5` — %d/%d recent drills, %.0f%% within tolerance", r.Estimate.Window, c.Policy.EstimateMasteryWindow, r.Estimate.WindowRate*100)
		if len(r.Estimate.KindsMissing) > 0 {
			msg += "; not yet tried: " + strings.Join(r.Estimate.KindsMissing, ", ")
		}
		next = append(next, msg+".")
	}
	if len(next) == 0 {
		next = append(next, "Nothing pending. Run `sd review` or a mock with the tutor.")
	}
	return next
}

func findModule(ms []ModuleStatus, id string) *ModuleStatus {
	for i := range ms {
		if ms[i].ID == id {
			return &ms[i]
		}
	}
	return nil
}

// RecordQuiz appends a quiz attempt, clears a refresh flag on a passing score, and reconciles gates.
func RecordQuiz(c *course.Course, p *progress.Progress, moduleID string, a progress.QuizAttempt) []string {
	rec := p.Module(moduleID)
	rec.QuizAttempts = append(rec.QuizAttempts, a)
	if rec.NeedsRefresh && a.Score+1e-9 >= c.Policy.QuizPassThreshold {
		rec.NeedsRefresh = false
		rec.RefreshReason = ""
		t := a.At
		rec.RefreshedAt = &t
	}
	p.Touch(a.At, "quiz", moduleID, fmt.Sprintf("%d/%d (%.0f%%)", a.Correct, a.Total, a.Score*100))
	return Reconcile(c, p, a.At)
}

// CompleteQuiz records a finished quiz run: every answer joins the review deck, a
// full-length (gated) attempt is recorded through RecordQuiz, and a shorter one is
// logged as practice. It returns the score and the gates that passed just now.
func CompleteQuiz(c *course.Course, p *progress.Progress, moduleID string, answers []progress.AnswerRecord, gated bool, now time.Time) (float64, []string) {
	correct := 0
	for _, ans := range answers {
		review.Enroll(p, c.Policy, moduleID, ans.QID, ans.Correct, now)
		if ans.Correct {
			correct++
		}
	}
	score := 0.0
	if len(answers) > 0 {
		score = float64(correct) / float64(len(answers))
	}
	if !gated {
		p.Touch(now, "quiz_practice", moduleID, fmt.Sprintf("%d/%d", correct, len(answers)))
		return score, nil
	}
	return score, RecordQuiz(c, p, moduleID, progress.QuizAttempt{At: now, Correct: correct, Total: len(answers), Score: score, Answers: answers})
}

// RecordLabAttempt appends a single-package lab run (web UI). It never affects the gate.
func RecordLabAttempt(p *progress.Progress, moduleID string, a progress.LabAttempt) {
	rec := p.Module(moduleID)
	rec.LabAttempts = append(rec.LabAttempts, a)
	p.Touch(a.At, "lab_attempt", moduleID, a.Package+" "+map[bool]string{true: "pass", false: "fail"}[a.Passed])
}

// RecordLab appends a lab run and reconciles gates.
func RecordLab(c *course.Course, p *progress.Progress, moduleID string, run progress.LabRun) []string {
	rec := p.Module(moduleID)
	rec.LabRuns = append(rec.LabRuns, run)
	p.Touch(run.At, "lab", moduleID, map[bool]string{true: "pass", false: "fail"}[run.Passed])
	return Reconcile(c, p, run.At)
}

// RecordDesign validates rubric levels, appends a design assessment and reconciles gates.
func RecordDesign(c *course.Course, p *progress.Progress, moduleID string, a progress.Assessment) ([]string, error) {
	for _, lvl := range []string{a.Self, a.Tutor} {
		if lvl != "" && c.Policy.LevelRank(lvl) < 0 {
			return nil, fmt.Errorf("unknown rubric level %q (use one of %s)", lvl, strings.Join(c.Policy.RubricLevels, ", "))
		}
	}
	if a.Self == "" && a.Tutor == "" {
		return nil, fmt.Errorf("record at least --self or --tutor")
	}
	a.Self, a.Tutor = strings.ToLower(a.Self), strings.ToLower(a.Tutor)
	rec := p.Module(moduleID)
	// A self-score submitted before the tutor's critique is merged with the tutor's
	// score when it arrives the same day, so the pair forms one assessment.
	if n := len(rec.Designs); n > 0 && a.Self == "" {
		last := &rec.Designs[n-1]
		if last.Tutor == "" && progress.Date(last.At) == progress.Date(a.At) {
			last.Tutor = a.Tutor
			if a.Notes != "" {
				last.Notes = strings.TrimSpace(last.Notes + " " + a.Notes)
			}
			p.Touch(a.At, "design", moduleID, "tutor="+a.Tutor)
			return Reconcile(c, p, a.At), nil
		}
	}
	rec.Designs = append(rec.Designs, a)
	p.Touch(a.At, "design", moduleID, fmt.Sprintf("self=%s tutor=%s", orDash(a.Self), orDash(a.Tutor)))
	return Reconcile(c, p, a.At), nil
}

// RecordTeachback appends a teach-back verdict and reconciles gates.
func RecordTeachback(c *course.Course, p *progress.Progress, moduleID string, a progress.Assessment) ([]string, error) {
	a.Result = strings.ToLower(a.Result)
	if a.Result != "pass" && a.Result != "revise" {
		return nil, fmt.Errorf("teach-back result must be pass or revise, got %q", a.Result)
	}
	rec := p.Module(moduleID)
	rec.Teachbacks = append(rec.Teachbacks, a)
	p.Touch(a.At, "teachback", moduleID, a.Result)
	return Reconcile(c, p, a.At), nil
}

// RecordMock appends a mock interview result and reconciles gates. It refuses a mock for
// a module whose gate has no mocks component, a module that is still locked (mocks only
// count once the prerequisites' gates have passed), and a format the module doesn't accept.
func RecordMock(c *course.Course, p *progress.Progress, moduleID string, mk progress.Mock) ([]string, error) {
	m, err := c.Module(moduleID)
	if err != nil {
		return nil, err
	}
	if !m.Requires(course.GateMocks) {
		return nil, fmt.Errorf("%s is not a mock module (mocks are recorded against Track D modules)", m.ID)
	}
	if ok, missing := Unlocked(c, m, p); !ok {
		return nil, fmt.Errorf("%s is locked (needs %s); mocks only count once unlocked", m.ID, strings.Join(missing, ", "))
	}
	mk.Format = strings.ToLower(strings.TrimSpace(mk.Format))
	mk.Problem = strings.TrimSpace(mk.Problem)
	if mk.Format == "" || mk.Problem == "" || mk.Minutes <= 0 {
		return nil, fmt.Errorf("a mock needs a format, a problem and its minutes")
	}
	if !m.AcceptsMockFormat(mk.Format) {
		want := m.Gate.MockFormats
		if len(want) == 0 {
			want = course.MockFormats
		}
		return nil, fmt.Errorf("%s mocks use format %s, not %q", m.ID, strings.Join(want, " or "), mk.Format)
	}
	if c.Policy.LevelRank(mk.Level) < 0 {
		return nil, fmt.Errorf("unknown rubric level %q (use one of %s)", mk.Level, strings.Join(c.Policy.RubricLevels, ", "))
	}
	mk.Level = strings.ToLower(mk.Level)
	rec := p.Module(m.ID)
	rec.Mocks = append(rec.Mocks, mk)
	p.Touch(mk.At, "mock", m.ID, fmt.Sprintf("%s %s %s", mk.Format, mk.Problem, mk.Level))
	return Reconcile(c, p, mk.At), nil
}

// CheckRefresh flags passed modules whose recent review accuracy fell below the policy
// threshold. It returns the ids newly flagged.
func CheckRefresh(c *course.Course, p *progress.Progress, now time.Time) []string {
	pol := c.Policy
	byModule := map[string][]progress.ReviewEntry{}
	for _, e := range p.Review.Log {
		byModule[e.Module] = append(byModule[e.Module], e)
	}
	var flagged []string
	ids := make([]string, 0, len(byModule))
	for id := range byModule {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		rec := p.Peek(id)
		if rec == nil || rec.GatePassedAt == nil || rec.NeedsRefresh {
			continue
		}
		var win []progress.ReviewEntry
		for _, e := range byModule[id] {
			// Only reviews since the last refresh count against the module.
			if rec.RefreshedAt == nil || e.At.After(*rec.RefreshedAt) {
				win = append(win, e)
			}
		}
		if len(win) > pol.ReviewRefreshWindow {
			win = win[len(win)-pol.ReviewRefreshWindow:]
		}
		if len(win) < pol.ReviewRefreshMinItems {
			continue
		}
		ok := 0
		for _, e := range win {
			if e.Correct {
				ok++
			}
		}
		acc := float64(ok) / float64(len(win))
		if acc+1e-9 < pol.ReviewRefreshThreshold {
			rec.NeedsRefresh = true
			rec.RefreshReason = fmt.Sprintf("last %d reviews %.0f%% < %.0f%%", len(win), acc*100, pol.ReviewRefreshThreshold*100)
			t := now
			rec.RefreshFlaggedAt = &t
			flagged = append(flagged, id)
			p.Touch(now, "needs_refresh", id, rec.RefreshReason)
		}
	}
	return flagged
}
