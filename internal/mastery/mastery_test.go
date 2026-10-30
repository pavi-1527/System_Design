package mastery

import (
	"strings"
	"testing"
	"time"

	"github.com/chethu007/system-design-course/internal/course"
	"github.com/chethu007/system-design-course/internal/progress"
)

func testCourse() *course.Course {
	c := &course.Course{Modules: map[string]*course.Module{}, RecommendedOrder: []string{"A0", "A1", "D1"}}
	c.Policy = course.Policy{
		QuizPassThreshold: 0.85, QuizPassesRequired: 2, QuizPassesDistinctDays: true,
		RubricLevels: []string{"below", "approaching", "meets", "exceeds"}, DesignMinLevel: "meets", MockMinLevel: "meets",
		ReviewIntervalsDays: []int{1, 3, 7}, ReviewRefreshThreshold: 0.7, ReviewRefreshWindow: 10, ReviewRefreshMinItems: 5,
		ReviewOverdueDays: 45, RecapGapDays: 14, EstimateMasteryWindow: 20, EstimateMasteryRate: 0.8,
	}
	full := course.Gate{Requires: []string{"quiz", "lab", "design", "teachback"}}
	c.Modules["A0"] = &course.Module{ID: "A0", Title: "Zero", Track: "A", Status: "written", Core: true, Gate: full,
		Labs: []course.Lab{{Package: "x", Core: true}}, Hours: course.Hours{Core: 10, Stretch: 2}}
	c.Modules["A1"] = &course.Module{ID: "A1", Title: "One", Track: "A", Status: "written", Core: true, Gate: full,
		Prerequisites: []string{"A0"}, Labs: []course.Lab{{Package: "y", Core: true}}}
	c.Modules["D1"] = &course.Module{ID: "D1", Title: "Mock", Track: "D", Status: "planned", Core: true,
		Gate: course.Gate{Requires: []string{"mocks"}, MocksRequired: 2}, Prerequisites: []string{"A0"}}
	return c
}

var day0 = time.Date(2026, 10, 1, 10, 0, 0, 0, time.Local)

func attempt(at time.Time, score float64) progress.QuizAttempt {
	return progress.QuizAttempt{At: at, Correct: int(score * 20), Total: 20, Score: score}
}

func TestQuizNeedsTwoPassesOnDifferentDays(t *testing.T) {
	c, p := testCourse(), progress.New()
	RecordQuiz(c, p, "A0", attempt(day0, 0.9))
	RecordQuiz(c, p, "A0", attempt(day0.Add(2*time.Hour), 0.95))
	if quizComponent(c.Policy, c.Modules["A0"], p.Module("A0")).Met {
		t.Fatal("two passes on the same day must not satisfy the quiz gate")
	}
	RecordQuiz(c, p, "A0", attempt(day0.AddDate(0, 0, 1), 0.80))
	if quizComponent(c.Policy, c.Modules["A0"], p.Module("A0")).Met {
		t.Fatal("80% is below the 85% pass mark")
	}
	RecordQuiz(c, p, "A0", attempt(day0.AddDate(0, 0, 2), 0.85))
	if !quizComponent(c.Policy, c.Modules["A0"], p.Module("A0")).Met {
		t.Fatal("exactly 85% on a second day should pass")
	}
}

func passA0(t *testing.T, c *course.Course, p *progress.Progress) {
	t.Helper()
	RecordQuiz(c, p, "A0", attempt(day0, 0.9))
	RecordQuiz(c, p, "A0", attempt(day0.AddDate(0, 0, 1), 0.9))
	RecordLab(c, p, "A0", progress.LabRun{At: day0, Passed: true, Race: true, Packages: []progress.LabPackage{{Package: "x", Core: true, Passed: true}}})
	if _, err := RecordDesign(c, p, "A0", progress.Assessment{At: day0, Self: "meets"}); err != nil {
		t.Fatal(err)
	}
	if p.Module("A0").GatePassedAt != nil {
		t.Fatal("gate passed without tutor score and teach-back")
	}
	if _, err := RecordDesign(c, p, "A0", progress.Assessment{At: day0, Tutor: "approaching"}); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordTeachback(c, p, "A0", progress.Assessment{At: day0, Result: "pass"}); err != nil {
		t.Fatal(err)
	}
	if p.Module("A0").GatePassedAt != nil {
		t.Fatal("tutor score 'approaching' must not pass the design gate")
	}
	passed, err := RecordDesign(c, p, "A0", progress.Assessment{At: day0.AddDate(0, 0, 2), Self: "meets", Tutor: "meets"})
	if err != nil {
		t.Fatal(err)
	}
	if len(passed) != 1 || passed[0] != "A0" {
		t.Fatalf("expected A0 to pass now, got %v", passed)
	}
}

func TestGateUnlocksDependents(t *testing.T) {
	c, p := testCourse(), progress.New()
	if st := Status(c, c.Modules["A1"], p, day0); st.State != Locked {
		t.Fatalf("A1 should be locked, got %s", st.State)
	}
	passA0(t, c, p)
	if st := Status(c, c.Modules["A1"], p, day0); st.State != Available {
		t.Fatalf("A1 should be available after A0 passes, got %s", st.State)
	}
	if st := Status(c, c.Modules["D1"], p, day0); st.State != Planned {
		t.Fatalf("D1 is unlocked but unwritten: want planned, got %s", st.State)
	}
}

func TestDesignMergesSameDayTutorScore(t *testing.T) {
	c, p := testCourse(), progress.New()
	RecordDesign(c, p, "A0", progress.Assessment{At: day0, Self: "meets"})
	RecordDesign(c, p, "A0", progress.Assessment{At: day0.Add(time.Hour), Tutor: "meets"})
	if n := len(p.Module("A0").Designs); n != 1 {
		t.Fatalf("self + tutor on the same day should merge into one record, got %d", n)
	}
	if !designComponent(c.Policy, c.Modules["A0"], p.Module("A0")).Met {
		t.Fatal("merged self=meets tutor=meets should meet the design gate")
	}
	if _, err := RecordDesign(c, p, "A0", progress.Assessment{At: day0, Self: "great"}); err == nil {
		t.Fatal("unknown rubric level should be rejected")
	}
}

func TestReviewDropFlagsNeedsRefreshAndQuizClearsIt(t *testing.T) {
	c, p := testCourse(), progress.New()
	passA0(t, c, p)
	later := day0.AddDate(0, 1, 0)
	for i := 0; i < 10; i++ {
		p.Review.Log = append(p.Review.Log, progress.ReviewEntry{At: later, QID: "A0-001", Module: "A0", Correct: i < 6})
	}
	flagged := CheckRefresh(c, p, later)
	if len(flagged) != 1 {
		t.Fatalf("60%% review accuracy should flag A0, got %v", flagged)
	}
	if st := Status(c, c.Modules["A0"], p, later); st.State != NeedsRefresh {
		t.Fatalf("want needs_refresh, got %s", st.State)
	}
	if st := Status(c, c.Modules["A1"], p, later); st.State == Locked {
		t.Fatal("a refresh flag must not re-lock dependents")
	}
	RecordQuiz(c, p, "A0", attempt(later.Add(time.Hour), 0.9))
	if p.Module("A0").NeedsRefresh {
		t.Fatal("a passing quiz should clear the refresh flag")
	}
	// Old bad reviews from before the refresh must not re-flag it.
	if f := CheckRefresh(c, p, later.Add(2*time.Hour)); len(f) != 0 {
		t.Fatalf("re-flagged from stale reviews: %v", f)
	}
}

func TestMocksGate(t *testing.T) {
	c, p := testCourse(), progress.New()
	c.Modules["D1"].Status = "written"
	passA0(t, c, p)
	record := func(problem, level string) {
		t.Helper()
		if _, err := RecordMock(c, p, "D1", progress.Mock{At: day0, Format: "lld-45", Problem: problem, Minutes: 45, Level: level}); err != nil {
			t.Fatal(err)
		}
	}
	record("parking lot", "approaching")
	record("elevator", "meets")
	if mocksComponent(c.Policy, c.Modules["D1"], p.Module("D1")).Met {
		t.Fatal("only one mock at meets; need two")
	}
	record("vending", "exceeds")
	if !mocksComponent(c.Policy, c.Modules["D1"], p.Module("D1")).Met {
		t.Fatal("two mocks at ≥ meets should satisfy the gate")
	}
}

func TestMockRefusedWhileLocked(t *testing.T) {
	c, p := testCourse(), progress.New()
	c.Modules["D1"].Status = "written"
	_, err := RecordMock(c, p, "D1", progress.Mock{At: day0, Format: "lld-45", Problem: "atm", Minutes: 45, Level: "meets"})
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("a mock for a locked module must be refused (prerequisite A0 not passed), got err=%v", err)
	}
	if len(p.Module("D1").Mocks) != 0 {
		t.Fatal("a refused mock must not be stored")
	}
}

func TestMockFormatMustMatchModule(t *testing.T) {
	c, p := testCourse(), progress.New()
	c.Modules["D1"].Status = "written"
	c.Modules["D1"].Gate.MockFormats = []string{"lld-45"}
	passA0(t, c, p)
	for _, f := range []string{"hld-45", "whiteboard", ""} {
		if _, err := RecordMock(c, p, "D1", progress.Mock{At: day0, Format: f, Problem: "atm", Minutes: 45, Level: "meets"}); err == nil {
			t.Errorf("format %q recorded against an lld-45 module; it would count an HLD round towards the LLD gate", f)
		}
	}
	if _, err := RecordMock(c, p, "D1", progress.Mock{At: day0, Format: "LLD-45", Problem: "atm", Minutes: 45, Level: "meets"}); err != nil {
		t.Fatalf("format matching should ignore case: %v", err)
	}
	if got := p.Module("D1").Mocks[0].Format; got != "lld-45" {
		t.Fatalf("format stored as %q, want it normalised to lld-45", got)
	}
	// A mock stored in another format (e.g. hand-repaired file) never counts.
	p.Module("D1").Mocks = append(p.Module("D1").Mocks, progress.Mock{At: day0, Format: "hld-45", Problem: "feed", Minutes: 45, Level: "exceeds"})
	if mocksComponent(c.Policy, c.Modules["D1"], p.Module("D1")).Met {
		t.Fatal("an hld-45 mock must not count towards an lld-45 gate")
	}
}

func TestMocksDistinctProblems(t *testing.T) {
	c, p := testCourse(), progress.New()
	c.Modules["D1"].Status = "written"
	passA0(t, c, p)
	for _, problem := range []string{"ATM", " atm ", "Rule  engine"} {
		if _, err := RecordMock(c, p, "D1", progress.Mock{At: day0, Format: "lld-45", Problem: problem, Minutes: 45, Level: "meets"}); err != nil {
			t.Fatal(err)
		}
	}
	c.Policy.MockDistinctProblems = false
	if !mocksComponent(c.Policy, c.Modules["D1"], p.Module("D1")).Met {
		t.Fatal("without the distinct-problems policy, three passing mocks satisfy a 2-mock gate")
	}
	c.Policy.MockDistinctProblems = true
	comp := mocksComponent(c.Policy, c.Modules["D1"], p.Module("D1"))
	if !comp.Met {
		t.Fatalf("ATM and rule engine are two distinct problems: %s", comp.Detail)
	}
	p.Module("D1").Mocks = p.Module("D1").Mocks[:2]
	comp = mocksComponent(c.Policy, c.Modules["D1"], p.Module("D1"))
	if comp.Met {
		t.Fatalf("repeating the same problem (ATM twice) must count once under the distinct-problems policy: %s", comp.Detail)
	}
	if !strings.Contains(comp.Detail, "1/2") || !strings.Contains(comp.Detail, "distinct") {
		t.Fatalf("detail should say 1/2 on distinct problems, got %q", comp.Detail)
	}
}

func TestRecapAfterGap(t *testing.T) {
	c, p := testCourse(), progress.New()
	p.Touch(day0, "quiz", "A0", "")
	if r := Evaluate(c, p, day0.AddDate(0, 0, 14)); r.RecapDue {
		t.Fatal("exactly 14 days is not more than the 14-day threshold")
	}
	r := Evaluate(c, p, day0.AddDate(0, 0, 20))
	if !r.RecapDue || r.GapDays != 20 {
		t.Fatalf("want recap due after 20 days, got due=%v gap=%d", r.RecapDue, r.GapDays)
	}
	if len(r.Next) == 0 {
		t.Fatal("expected recommendations")
	}
}

func TestEstimateMastery(t *testing.T) {
	c, p := testCourse(), progress.New()
	for i := 0; i < 20; i++ {
		p.Estimate.Attempts = append(p.Estimate.Attempts, progress.EstimateAttempt{Kind: EstimateKinds[i%len(EstimateKinds)], Within: i%5 != 0})
	}
	if es := EstimateSummary(c.Policy, p); !es.Mastered {
		t.Fatalf("16/20 across all kinds should be mastered: %+v", es)
	}
	p.Estimate.Attempts[1].Within = false
	if es := EstimateSummary(c.Policy, p); es.Mastered {
		t.Fatalf("15/20 should not be mastered: %+v", es)
	}
}
