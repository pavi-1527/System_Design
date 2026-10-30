// Package quiz draws questions from a module's pool, shuffles choices, and grades answers.
// It has no I/O: callers (CLI, a future web UI) present Items and pass back answers.
package quiz

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"

	"github.com/chethu007/system-design-course/internal/course"
	"github.com/chethu007/system-design-course/internal/progress"
	"github.com/chethu007/system-design-course/internal/quantity"
)

// Item is one question as presented: choices are shown in Order (display index → original index).
type Item struct {
	Q     *course.Question `json:"question"`
	Order []int            `json:"order,omitempty"`
}

// Choices returns the choices in display order.
func (it Item) Choices() []string {
	out := make([]string, len(it.Order))
	for d, o := range it.Order {
		out[d] = it.Q.Choices[o]
	}
	return out
}

// Result is the graded outcome of one answer.
type Result struct {
	Correct     bool   `json:"correct"`
	Given       string `json:"given"`
	Expected    string `json:"expected"`
	Note        string `json:"note,omitempty"`
	Explanation string `json:"explanation"`
	Ref         string `json:"ref"`
}

// Prepare shuffles a question's choices.
func Prepare(q *course.Question, rng *rand.Rand) Item {
	it := Item{Q: q}
	if q.Type != course.QNumeric {
		it.Order = rng.Perm(len(q.Choices))
	}
	return it
}

// FullLength is the number of questions in a gated attempt: the quiz's draw (or the
// policy default), capped at the pool size. Shorter attempts are practice.
func FullLength(qz *course.Quiz, defaultDraw int) int {
	full := qz.Draw
	if full == 0 {
		full = defaultDraw
	}
	return min(full, len(qz.Questions))
}

// Seen counts how often each question appeared in the module's recorded attempts, so
// Draw can prefer the least-seen ones.
func Seen(rec *progress.ModuleRecord) map[string]int {
	seen := map[string]int{}
	if rec != nil {
		for _, at := range rec.QuizAttempts {
			for _, ans := range at.Answers {
				seen[ans.QID]++
			}
		}
	}
	return seen
}

// Skip grades a skipped question: always incorrect, with the expected answer shown.
func (it Item) Skip() Result {
	var res Result
	if it.Q.Type == course.QNumeric {
		res, _ = it.GradeNumeric("0")
		res.Correct = false
	} else {
		res = it.GradeChoices(nil)
	}
	res.Given = "(skipped)"
	return res
}

// CorrectDisplay returns the 1-based display positions of the correct choices.
func (it Item) CorrectDisplay() []int {
	want := map[int]bool{}
	for _, a := range it.Q.Answer {
		want[a] = true
	}
	var out []int
	for d, o := range it.Order {
		if want[o] {
			out = append(out, d+1)
		}
	}
	return out
}

// Draw picks n questions from the pool, preferring the ones seen least often
// (seen maps question id → times asked before), with random tie-breaks.
func Draw(qz *course.Quiz, n int, seen map[string]int, rng *rand.Rand) []Item {
	pool := make([]*course.Question, len(qz.Questions))
	for i := range qz.Questions {
		pool[i] = &qz.Questions[i]
	}
	rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	sort.SliceStable(pool, func(i, j int) bool { return seen[pool[i].ID] < seen[pool[j].ID] })
	if n <= 0 || n > len(pool) {
		n = len(pool)
	}
	pool = pool[:n]
	rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	items := make([]Item, n)
	for i, q := range pool {
		items[i] = Prepare(q, rng)
	}
	return items
}

// Grade parses a free-text answer: "2" for single choice, "1,3" or "1 3" for multi
// (1-based display indices), or a quantity like "1.2k" / "40 TB" for numeric.
func (it Item) Grade(input string) (Result, error) {
	if it.Q.Type == course.QNumeric {
		return it.GradeNumeric(input)
	}
	fields := strings.FieldsFunc(input, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
	if len(fields) == 0 {
		return Result{}, fmt.Errorf("enter a choice number (1-%d)", len(it.Order))
	}
	var picks []int
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 || n > len(it.Order) {
			return Result{}, fmt.Errorf("%q is not a choice number (1-%d)", f, len(it.Order))
		}
		picks = append(picks, n)
	}
	if it.Q.Type == course.QSingle && len(picks) != 1 {
		return Result{}, fmt.Errorf("pick exactly one choice")
	}
	return it.GradeChoices(picks), nil
}

// GradeChoices grades 1-based display indices.
func (it Item) GradeChoices(display []int) Result {
	got := map[int]bool{}
	for _, d := range display {
		if d >= 1 && d <= len(it.Order) {
			got[it.Order[d-1]] = true
		}
	}
	want := map[int]bool{}
	for _, a := range it.Q.Answer {
		want[a] = true
	}
	correct := len(got) == len(want)
	for k := range want {
		if !got[k] {
			correct = false
		}
	}
	return Result{
		Correct:     correct,
		Given:       it.label(got),
		Expected:    it.label(want),
		Explanation: it.Q.Explanation,
		Ref:         it.Q.Ref,
	}
}

func (it Item) label(orig map[int]bool) string {
	var parts []string
	for d, o := range it.Order {
		if orig[o] {
			parts = append(parts, fmt.Sprintf("%d) %s", d+1, it.Q.Choices[o]))
		}
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, " | ")
}

// GradeNumeric grades a quantity answer within the question's relative tolerance.
func (it Item) GradeNumeric(input string) (Result, error) {
	u := quantity.ParseUnit(it.Q.Unit)
	got, note, err := u.Parse(input)
	if err != nil {
		return Result{}, err
	}
	want := it.Q.Value * u.Scale
	tol := it.Q.Tolerance
	if tol <= 0 {
		tol = 0.1
	}
	r := Result{
		Correct:     quantity.Within(got, want, tol),
		Given:       describe(got, u),
		Expected:    fmt.Sprintf("%s (accepting ±%.0f%%)", describe(want, u), tol*100),
		Note:        note,
		Explanation: it.Q.Explanation,
		Ref:         it.Q.Ref,
	}
	return r, nil
}

func describe(base float64, u quantity.Unit) string {
	s := u.Format(base)
	if u.Dim == quantity.Count && u.Label != "" {
		s += " " + u.Label
	}
	return s
}

// Score is the fraction correct.
func Score(correct, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(correct) / float64(total)
}
