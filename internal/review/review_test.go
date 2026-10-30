package review

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/chethu007/system-design-course/internal/course"
	"github.com/chethu007/system-design-course/internal/progress"
)

var pol = course.Policy{ReviewIntervalsDays: []int{1, 3, 7, 14}}

func TestLeitnerSchedule(t *testing.T) {
	p := progress.New()
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.Local)
	Enroll(p, pol, "A0", "A0-001", true, now)
	Enroll(p, pol, "A0", "A0-002", false, now)
	if c := p.Review.Cards["A0-001"]; c.Box != 1 || c.Due != "2026-10-04" {
		t.Fatalf("correct new card: want box 1 due +3d, got %+v", c)
	}
	if c := p.Review.Cards["A0-002"]; c.Box != 0 || c.Due != "2026-10-02" {
		t.Fatalf("missed new card: want box 0 due +1d, got %+v", c)
	}
	idx := map[string]*course.Question{"A0-001": {ID: "A0-001"}, "A0-002": {ID: "A0-002"}}
	rng := rand.New(rand.NewPCG(1, 1))
	if d := Due(p, idx, now.AddDate(0, 0, 1), 10, rng); len(d) != 1 || d[0] != "A0-002" {
		t.Fatalf("day+1 due: %v", d)
	}
	Answer(p, pol, "A0-002", true, now.AddDate(0, 0, 1))
	if c := p.Review.Cards["A0-002"]; c.Box != 1 || c.Due != "2026-10-05" {
		t.Fatalf("after correct review: %+v", c)
	}
	Answer(p, pol, "A0-001", false, now.AddDate(0, 0, 3))
	if c := p.Review.Cards["A0-001"]; c.Box != 0 || c.Lapses != 1 || c.Due != "2026-10-05" {
		t.Fatalf("after missed review: %+v", c)
	}
	// Box is capped at the last interval.
	for i := 0; i < 10; i++ {
		Answer(p, pol, "A0-001", true, now)
	}
	if c := p.Review.Cards["A0-001"]; c.Box != len(pol.ReviewIntervalsDays)-1 {
		t.Fatalf("box should cap, got %d", c.Box)
	}
	// Retired questions are never served.
	delete(idx, "A0-002")
	if d := Due(p, idx, now.AddDate(1, 0, 0), 10, rng); len(d) != 1 {
		t.Fatalf("retired question served: %v", d)
	}
}

func TestQuizCorrectDoesNotSkipReview(t *testing.T) {
	p := progress.New()
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.Local)
	Enroll(p, pol, "A0", "A0-001", false, now)
	Enroll(p, pol, "A0", "A0-001", true, now.Add(time.Hour))
	if c := p.Review.Cards["A0-001"]; c.Box != 0 {
		t.Fatalf("a correct quiz answer must not promote an existing card: %+v", c)
	}
}

func TestDueInterleavesModules(t *testing.T) {
	p := progress.New()
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.Local)
	idx := map[string]*course.Question{}
	for _, id := range []string{"A0-001", "A0-002", "A0-003", "B0-001", "B0-002"} {
		Enroll(p, pol, id[:2], id, false, now)
		idx[id] = &course.Question{ID: id}
	}
	d := Due(p, idx, now.AddDate(0, 0, 1), 0, rand.New(rand.NewPCG(3, 3)))
	if len(d) != 5 {
		t.Fatalf("want 5 due, got %v", d)
	}
	for i := 1; i < 4; i++ {
		if d[i][:2] == d[i-1][:2] {
			t.Fatalf("consecutive cards from the same module while others remain: %v", d)
		}
	}
}
