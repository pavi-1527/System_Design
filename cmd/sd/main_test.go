package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chethu007/system-design-course/internal/progress"
)

// fixture writes a tiny but complete course: one written module T0 with a quiz,
// a lesson with predict/reveal markup, and a lab whose tests pass.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/fixture\n\ngo 1.22\n",
		"course/course.json": `{"schema_version":1,"title":"Fixture","tracks":[{"id":"T","title":"Test"}],
			"policy":{"quiz_pass_threshold":0.85,"quiz_passes_required":2,"quiz_passes_distinct_days":true},
			"recommended_order":["T0","T1"]}`,
		"modules/T0-zero/module.json": `{"schema_version":1,"id":"T0","slug":"zero","title":"Zero","track":"T","status":"written","core":true,
			"summary":"s","hours":{"core":5,"stretch":1,"total":6},"prerequisites":[],
			"gate":{"requires":["quiz","lab","design","teachback"]},
			"paths":{"dir":"modules/T0-zero","readme":"modules/T0-zero/README.md","quiz":"modules/T0-zero/quiz.json",
			  "design_exercise":"modules/T0-zero/design-exercise.md","teachback":"modules/T0-zero/teachback.md","lab":"labs/t0","solution":"solutions/t0"},
			"units":[{"n":1,"title":"U","file":"modules/T0-zero/unit-1-u.md","hours":{"core":5,"stretch":1},"labs":["adder"]}],
			"labs":[{"package":"adder","title":"Adder","unit":1,"core":true}]}`,
		"modules/T1-one/module.json": `{"schema_version":1,"id":"T1","slug":"one","title":"One","track":"T","status":"planned","core":true,
			"summary":"s","hours":{"core":6,"stretch":0},"prerequisites":["T0"],"gate":{"requires":["quiz"]},"paths":{"dir":"modules/T1-one"}}`,
		"modules/T0-zero/README.md":          "# T0\n",
		"modules/T0-zero/design-exercise.md": "# design\n",
		"modules/T0-zero/teachback.md":       "# teachback\n",
		"modules/T0-zero/unit-1-u.md": "# U\n<!-- predict id=\"T0-U1-P01\" -->\n**Predict.** 2+2?\n<!-- /predict -->\n\n" +
			"<details data-kind=\"reveal\" data-for=\"T0-U1-P01\">\n<summary>Reveal</summary>\n\n4\n\n</details>\n\n" +
			"<details data-kind=\"answer\"><summary>Check</summary>\n\nok\n\n</details>\n",
		"modules/T0-zero/quiz.json": `{"module":"T0","title":"Zero","draw":2,"questions":[
			{"id":"T0-001","type":"single","tags":["math"],"prompt":"2+2?","choices":["4","5"],"answer":[0],"explanation":"e","ref":"unit-1-u.md"},
			{"id":"T0-002","type":"numeric","tags":["estimation"],"prompt":"1B/day in req/s?","value":11574,"tolerance":0.25,"unit":"req/s","explanation":"e","ref":"unit-1-u.md"},
			{"id":"T0-003","type":"multi","tags":["math"],"prompt":"evens?","choices":["2","3","4"],"answer":[0,2],"explanation":"e","ref":"unit-1-u.md"},
			{"id":"T0-004","type":"single","tags":["math"],"prompt":"1+1?","choices":["2","3"],"answer":[0],"explanation":"e","ref":"unit-1-u.md"}]}`,
		"labs/t0/adder/adder.go":           "package adder\n\nfunc Add(a, b int) int { return a + b }\n",
		"labs/t0/adder/adder_test.go":      "package adder\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 2) != 4 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n",
		"solutions/t0/adder/adder.go":      "package adder\n\nfunc Add(a, b int) int { return a + b }\n",
		"solutions/t0/adder/adder_test.go": "package adder\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 2) != 4 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n",
	}
	for p, body := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("SD_ROOT", root)
	t.Setenv("SD_PROGRESS", filepath.Join(root, "progress.json"))
	return root
}

func sd(t *testing.T, now, stdin string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("SD_NOW", now)
	var out bytes.Buffer
	err := run(args, strings.NewReader(stdin), &out)
	return out.String(), err
}

func TestEndToEndGateFlow(t *testing.T) {
	root := fixture(t)

	out, err := sd(t, "2026-10-01T10:00:00Z", "", "validate")
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, out)
	}

	out, err = sd(t, "2026-10-01T10:00:00Z", "", "status")
	if err != nil || !strings.Contains(out, "T0") || !strings.Contains(out, "locked (needs T0)") {
		t.Fatalf("status: %v\n%s", err, out)
	}

	// A quiz with only 1 question is practice, not a gate attempt.
	if _, err := sd(t, "2026-10-01T10:00:00Z", "s\n", "quiz", "T0", "-n", "1"); err != nil {
		t.Fatal(err)
	}
	p, _ := progress.Load(filepath.Join(root, "progress.json"))
	if n := len(p.Module("T0").QuizAttempts); n != 0 {
		t.Fatalf("practice run recorded as gate attempt (%d)", n)
	}

	// Quitting mid-quiz records nothing.
	if _, err := sd(t, "2026-10-01T10:00:00Z", "q\n", "quiz", "T0"); err != errAborted {
		t.Fatalf("want errAborted, got %v", err)
	}

	// Record lab, design, teach-back.
	if out, err := sd(t, "2026-10-01T10:00:00Z", "", "lab", "T0", "--no-race"); err != nil {
		t.Fatalf("lab: %v\n%s", err, out)
	}
	// --no-race with cgo available is not recorded; record with race when possible.
	out, err = sd(t, "2026-10-01T10:00:00Z", "", "lab", "T0")
	if err != nil || !strings.Contains(out, "All core labs pass") {
		t.Fatalf("lab: %v\n%s", err, out)
	}
	if _, err := sd(t, "2026-10-01T10:00:00Z", "", "record", "design", "T0", "--self", "meets", "--tutor", "meets", "--notes", "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := sd(t, "2026-10-01T10:00:00Z", "", "record", "teachback", "T0", "--result", "pass"); err != nil {
		t.Fatal(err)
	}
	if _, err := sd(t, "2026-10-01T10:00:00Z", "", "record", "design", "T0", "--self", "superb"); err == nil {
		t.Fatal("bad rubric level accepted")
	}

	// Two perfect quiz attempts on different days pass the gate. We answer by
	// recognising the choice text in the printed question.
	for _, day := range []string{"2026-10-01T11:00:00Z", "2026-10-02T11:00:00Z"} {
		out := quizPerfect(t, day)
		if !strings.Contains(out, "PASS") {
			t.Fatalf("quiz on %s did not pass:\n%s", day, out)
		}
	}
	p, _ = progress.Load(filepath.Join(root, "progress.json"))
	if p.Module("T0").GatePassedAt == nil {
		t.Fatalf("gate should have passed: %+v", p.Module("T0"))
	}
	out, _ = sd(t, "2026-10-02T12:00:00Z", "", "status")
	if !strings.Contains(out, "PASSED") || !strings.Contains(out, "planned — unlocked") {
		t.Fatalf("status after gate:\n%s", out)
	}

	// After a long gap, status asks for a recap.
	out, _ = sd(t, "2026-11-20T12:00:00Z", "", "status")
	if !strings.Contains(out, "sd recap") {
		t.Fatalf("expected recap prompt after gap:\n%s", out)
	}
	if out, err := sd(t, "2026-11-20T12:00:00Z", "", "recap", "--no-quiz"); err != nil || !strings.Contains(out, "Recap") {
		t.Fatalf("recap: %v\n%s", err, out)
	}

	// JSON outputs are valid for other front ends.
	for _, args := range [][]string{{"status", "--json"}, {"progress", "--json"}, {"status", "T0", "--json"}, {"validate", "--json"}} {
		if out, err := sd(t, "2026-11-20T12:00:00Z", "", args...); err != nil || !strings.HasPrefix(strings.TrimSpace(out), "{") && !strings.HasPrefix(strings.TrimSpace(out), "[") {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
}

// quizPerfect runs a full quiz, answering each question correctly by reading the prompt.
func quizPerfect(t *testing.T, now string) string {
	t.Helper()
	// Choice order is shuffled per run, so answers are produced lazily by a reader
	// that inspects what was printed.
	t.Setenv("SD_NOW", now)
	var out bytes.Buffer
	r := &lazyAnswers{out: &out}
	if err := run([]string{"quiz", "T0"}, r, &out); err != nil {
		t.Fatalf("quiz: %v\n%s", err, out.String())
	}
	return out.String()
}

// lazyAnswers is an io.Reader that, when read, looks at the last printed question and
// returns the correct answer line for the fixture quiz.
type lazyAnswers struct {
	out     *bytes.Buffer
	pending []byte
	asked   int
}

func (l *lazyAnswers) Read(b []byte) (int, error) {
	if len(l.pending) == 0 {
		s := l.out.String()
		i := strings.LastIndex(s, "── Q")
		if i < 0 || strings.Count(s, "── Q") == l.asked {
			return 0, os.ErrClosed
		}
		l.asked = strings.Count(s, "── Q")
		q := s[i:]
		var ans string
		switch {
		case strings.Contains(q, "1B/day"):
			ans = "11.6k"
		case strings.Contains(q, "evens?"):
			ans = strings.Join(choiceNums(q, "2", "4"), ",")
		case strings.Contains(q, "2+2?"):
			ans = choiceNums(q, "4")[0]
		case strings.Contains(q, "1+1?"):
			ans = choiceNums(q, "2")[0]
		}
		l.pending = []byte(ans + "\n")
	}
	n := copy(b, l.pending)
	l.pending = l.pending[n:]
	return n, nil
}

func choiceNums(q string, texts ...string) []string {
	var out []string
	for _, line := range strings.Split(q, "\n") {
		line = strings.TrimSpace(line)
		for _, tx := range texts {
			if strings.HasSuffix(line, ") "+tx) {
				out = append(out, strings.SplitN(line, ")", 2)[0])
			}
		}
	}
	return out
}

func TestValidateCatchesBrokenMarkup(t *testing.T) {
	root := fixture(t)
	bad := "# U\n<!-- predict id=\"T0-U1-P01\" -->\nq\n<!-- /predict -->\n<details><summary>Reveal</summary>\nx\n</details>\n"
	if err := os.WriteFile(filepath.Join(root, "modules/T0-zero/unit-1-u.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "labs/t0/adder/adder_test.go"), []byte("package adder\n"), 0o644)
	out, err := sd(t, "2026-10-01T10:00:00Z", "", "validate")
	if err == nil {
		t.Fatalf("validate should fail:\n%s", out)
	}
	for _, want := range []string{"without data-kind", "has no <details", "byte-identical"} {
		if !strings.Contains(out, want) {
			t.Errorf("validate output missing %q:\n%s", want, out)
		}
	}
}
