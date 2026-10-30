package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chethu007/system-design-course/internal/progress"
)

// fixture writes a tiny course: written module T0 (quiz, lesson with a predict block,
// a passing lab with a solution) and planned module T1 that depends on it.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	adder := "package adder\n\nfunc Add(a, b int) int { return a + b }\n"
	adderTest := "package adder\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 2) != 4 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n"
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
			"<details data-kind=\"reveal\" data-for=\"T0-U1-P01\">\n<summary>Reveal</summary>\n\n4\n\n</details>\n",
		"modules/T0-zero/quiz.json": `{"module":"T0","title":"Zero","draw":2,"questions":[
			{"id":"T0-001","type":"single","tags":["math"],"prompt":"2+2?","choices":["4","5"],"answer":[0],"explanation":"e1","ref":"unit-1-u.md#u"},
			{"id":"T0-002","type":"numeric","tags":["estimation"],"prompt":"1B/day in req/s?","value":11574,"tolerance":0.25,"unit":"req/s","explanation":"e2","ref":"unit-1-u.md"},
			{"id":"T0-003","type":"multi","tags":["math"],"prompt":"evens?","choices":["2","3","4"],"answer":[0,2],"explanation":"e3","ref":"unit-1-u.md"},
			{"id":"T0-004","type":"single","tags":["math"],"prompt":"1+1?","choices":["2","3"],"answer":[0],"explanation":"e4","ref":"unit-1-u.md"}]}`,
		"labs/t0/README.md":                "# Labs\n",
		"labs/t0/adder/adder.go":           adder,
		"labs/t0/adder/adder_test.go":      adderTest,
		"solutions/t0/adder/adder.go":      adder,
		"solutions/t0/adder/adder_test.go": adderTest,
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
	return root
}

type harness struct {
	t    *testing.T
	root string
	now  time.Time
	h    http.Handler
}

func newHarness(t *testing.T) *harness {
	hs := &harness{t: t, root: fixture(t), now: time.Date(2026, 10, 1, 10, 0, 0, 0, time.Local)}
	srv := New(Config{Root: hs.root, ProgressPath: filepath.Join(hs.root, "progress.json"), Now: func() time.Time { return hs.now }})
	hs.h = srv.Handler()
	return hs
}

// do sends a request as the browser would (loopback Host, JSON body) and decodes the
// JSON reply into out when out is non-nil. It returns the status code.
func (hs *harness) do(method, path string, body, out any) int {
	hs.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Host = "localhost:8080"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	hs.h.ServeHTTP(rec, req)
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			hs.t.Fatalf("%s %s: bad JSON (%d): %v\n%s", method, path, rec.Code, err, rec.Body.String())
		}
	}
	return rec.Code
}

func (hs *harness) progress() *progress.Progress {
	hs.t.Helper()
	p, err := progress.Load(filepath.Join(hs.root, "progress.json"))
	if err != nil {
		hs.t.Fatal(err)
	}
	return p
}

func moduleState(t *testing.T, st statusResponse, id string) string {
	t.Helper()
	for _, m := range st.Modules {
		if m.ID == id {
			return string(m.State)
		}
	}
	t.Fatalf("module %s missing from status", id)
	return ""
}

func TestStatus_ReadsModulesFromMetadataOnEveryRequest(t *testing.T) {
	hs := newHarness(t)
	var st statusResponse
	if code := hs.do("GET", "/api/status", nil, &st); code != 200 {
		t.Fatalf("status code %d", code)
	}
	if got := moduleState(t, st, "T0"); got != "available" {
		t.Errorf("T0 state = %s, want available (no prerequisites, nothing recorded)", got)
	}
	if got := moduleState(t, st, "T1"); got != "locked" {
		t.Errorf("T1 state = %s, want locked behind T0", got)
	}
	if len(st.Report.Next) == 0 {
		t.Error("status should carry a next recommended step")
	}

	// A module written by another session appears without restarting the server.
	meta := `{"schema_version":1,"id":"T2","slug":"two","title":"Two","track":"T","status":"planned","core":false,
		"summary":"s","hours":{"core":1,"stretch":0},"prerequisites":[],"gate":{"requires":["quiz"]},"paths":{"dir":"modules/T2-two"}}`
	if err := os.MkdirAll(filepath.Join(hs.root, "modules/T2-two"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hs.root, "modules/T2-two/module.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	hs.do("GET", "/api/status", nil, &st)
	if got := moduleState(t, st, "T2"); got != "planned" {
		t.Errorf("new module T2 state = %q, want planned (it should be picked up from module.json)", got)
	}
}

func TestGuard_RejectsForeignHostsOriginsAndFormPosts(t *testing.T) {
	hs := newHarness(t)
	send := func(method, host, origin, ctype string) int {
		req := httptest.NewRequest(method, "/api/predictions/T0-U1-P01", strings.NewReader(`{"answer":"x"}`))
		req.Host = host
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		rec := httptest.NewRecorder()
		hs.h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := send("PUT", "evil.example:8080", "", "application/json"); c != http.StatusForbidden {
		t.Errorf("non-loopback Host got %d, want 403 (DNS rebinding)", c)
	}
	if c := send("PUT", "localhost:8080", "http://evil.example", "application/json"); c != http.StatusForbidden {
		t.Errorf("cross-origin write got %d, want 403", c)
	}
	if c := send("PUT", "localhost:8080", "", "text/plain"); c != http.StatusUnsupportedMediaType {
		t.Errorf("non-JSON write got %d, want 415 (blocks cross-site form posts)", c)
	}
	if c := send("PUT", "127.0.0.1:8080", "http://127.0.0.1:8080", "application/json"); c != http.StatusOK {
		t.Errorf("same-origin write got %d, want 200", c)
	}
	if p := hs.progress(); len(p.Module("T0").Predictions) != 1 {
		t.Errorf("only the same-origin write should have been saved: %+v", p.Module("T0").Predictions)
	}
}

func TestStaticAndClientRoutesServeTheApp(t *testing.T) {
	hs := newHarness(t)
	for _, path := range []string{"/", "/module/T0", "/doc/modules/T0-zero/unit-1-u.md", "/static/app.js", "/static/vendor/mermaid.min.js"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Host = "localhost:8080"
		rec := httptest.NewRecorder()
		hs.h.ServeHTTP(rec, req)
		if rec.Code != 200 || rec.Body.Len() == 0 {
			t.Errorf("GET %s = %d (%d bytes), want 200 with a body", path, rec.Code, rec.Body.Len())
		}
	}
	var e map[string]string
	if c := hs.do("GET", "/api/nope", nil, &e); c != 404 || e["error"] == "" {
		t.Errorf("unknown API path = %d %v, want a JSON 404", c, e)
	}
}

func TestDoc_PreparesPredictBlocksAndRefusesEscapes(t *testing.T) {
	hs := newHarness(t)
	var d docResponse
	if c := hs.do("GET", "/api/doc?path=modules/T0-zero/unit-1-u.md", nil, &d); c != 200 {
		t.Fatalf("doc code %d", c)
	}
	if d.Module != "T0" || len(d.PredictIDs) != 1 || d.PredictIDs[0] != "T0-U1-P01" {
		t.Fatalf("doc = %+v, want module T0 with one predict id", d)
	}
	if !strings.Contains(d.Markdown, `<div class="predict" data-predict-id="T0-U1-P01">`) {
		t.Errorf("predict block not converted for the browser:\n%s", d.Markdown)
	}
	for _, bad := range []string{"../etc/passwd.md", "/../../x.md", "go.mod", "modules/T0-zero/quiz.json", ".git/x.md", "modules/T0-zero/missing.md"} {
		if c := hs.do("GET", "/api/doc?path="+bad, nil, nil); c == 200 {
			t.Errorf("doc path %q was served; only Markdown inside the course root may be", bad)
		}
	}
}

func TestPredictions_RequireAnAnswerAndKeepTheFirstOne(t *testing.T) {
	hs := newHarness(t)
	if c := hs.do("PUT", "/api/predictions/T0-U1-P01", map[string]string{"answer": "   "}, nil); c != http.StatusBadRequest {
		t.Fatalf("empty prediction got %d, want 400: the reveal must stay closed until something is written", c)
	}
	if c := hs.do("PUT", "/api/predictions/ZZ-U1-P01", map[string]string{"answer": "x"}, nil); c != http.StatusBadRequest {
		t.Errorf("malformed predict id got %d, want 400", c)
	}
	var r predictResponse
	if c := hs.do("PUT", "/api/predictions/T0-U1-P01", map[string]string{"answer": "four"}, &r); c != 200 || !r.Created {
		t.Fatalf("save got %d %+v", c, r)
	}
	hs.do("PUT", "/api/predictions/T0-U1-P01", map[string]string{"answer": "edited after the reveal"}, &r)
	if r.Created || r.Prediction.Answer != "four" {
		t.Errorf("second save = %+v; the first prediction must be kept so it can be compared with the reveal", r)
	}
	var d docResponse
	hs.do("GET", "/api/doc?path=modules/T0-zero/unit-1-u.md", nil, &d)
	if d.Predictions["T0-U1-P01"] == nil || d.Predictions["T0-U1-P01"].Answer != "four" {
		t.Errorf("doc should return the saved prediction: %+v", d.Predictions)
	}
	var st statusResponse
	hs.do("GET", "/api/status", nil, &st)
	if got := moduleState(t, st, "T0"); got != "in_progress" {
		t.Errorf("T0 = %s after a prediction, want in_progress", got)
	}
	if p := hs.progress(); p.LastActiveAt == nil {
		t.Error("a prediction is learning activity and should bump last_active_at")
	}
}

// answerFor returns the request that answers a fixture question correctly.
func answerFor(it itemView) answerRequest {
	req := answerRequest{Index: it.Index}
	want := map[string][]string{"2+2?": {"4"}, "evens?": {"2", "4"}, "1+1?": {"2"}}[it.Prompt]
	if it.Type == "numeric" {
		req.Input = "11.6k"
		return req
	}
	for d, c := range it.Choices {
		for _, w := range want {
			if c == w {
				req.Choices = append(req.Choices, d+1)
			}
		}
	}
	return req
}

func takeQuiz(t *testing.T, hs *harness, n int, correct bool) *sessionSummary {
	t.Helper()
	var sess sessionView
	if c := hs.do("POST", "/api/sessions", map[string]any{"kind": "quiz", "module": "T0", "n": n}, &sess); c != 200 {
		t.Fatalf("start quiz: %d", c)
	}
	for _, it := range sess.Items {
		if it.Type != "numeric" && len(it.Choices) == 0 {
			t.Fatalf("choice question without choices: %+v", it)
		}
		req := answerFor(it)
		if !correct {
			req = answerRequest{Index: it.Index, Skip: true}
		}
		var ar answerResponse
		if c := hs.do("POST", "/api/sessions/"+sess.ID+"/answers", req, &ar); c != 200 {
			t.Fatalf("answer %d: code %d", it.Index, c)
		}
		if ar.Result.Correct != correct || ar.Result.Explanation == "" {
			t.Fatalf("answer %+v graded %+v; want correct=%v with an explanation", req, ar.Result, correct)
		}
		if it.Type != "numeric" && len(ar.Result.CorrectChoices) == 0 {
			t.Errorf("choice result should say which displayed choices were right: %+v", ar.Result)
		}
		if ar.Result.RefPath != "modules/T0-zero/unit-1-u.md" {
			t.Errorf("ref path = %q, want the repo-relative lesson file", ar.Result.RefPath)
		}
		if ar.Done {
			return ar.Summary
		}
	}
	t.Fatal("quiz never finished")
	return nil
}

func TestQuiz_FullAttemptsCountForTheGateAndPracticeDoesNot(t *testing.T) {
	hs := newHarness(t)

	if s := takeQuiz(t, hs, 1, true); s.Gated {
		t.Errorf("a 1-question run is practice, got gated summary %+v", s)
	}
	if n := len(hs.progress().Module("T0").QuizAttempts); n != 0 {
		t.Fatalf("practice recorded as a gate attempt (%d)", n)
	}
	if len(hs.progress().Review.Cards) != 1 {
		t.Error("practice answers should still join the review deck")
	}

	s := takeQuiz(t, hs, 0, true)
	if !s.Gated || !s.Pass || s.Total != 2 {
		t.Fatalf("full correct attempt summary = %+v, want gated pass of 2", s)
	}
	hs.now = hs.now.Add(24 * time.Hour)
	s = takeQuiz(t, hs, 0, true)
	met := false
	for _, c := range s.Gate {
		if c.Name == "quiz" {
			met = c.Met
		}
	}
	if !met {
		t.Errorf("two passing attempts on different days should meet the quiz component: %+v", s.Gate)
	}
	if n := len(hs.progress().Module("T0").QuizAttempts); n != 2 {
		t.Errorf("recorded %d gate attempts, want 2", n)
	}
}

func TestQuiz_QuittingRecordsNothingAndAnswersAreFinal(t *testing.T) {
	hs := newHarness(t)
	var sess sessionView
	hs.do("POST", "/api/sessions", map[string]any{"kind": "quiz", "module": "T0"}, &sess)
	first := answerFor(sess.Items[0])
	if c := hs.do("POST", "/api/sessions/"+sess.ID+"/answers", first, nil); c != 200 {
		t.Fatalf("answer: %d", c)
	}
	if c := hs.do("POST", "/api/sessions/"+sess.ID+"/answers", first, nil); c != http.StatusConflict {
		t.Errorf("answering the same question twice got %d, want 409", c)
	}
	var fin map[string]any
	hs.do("POST", "/api/sessions/"+sess.ID+"/finish", map[string]any{}, &fin)
	if fin["recorded"] != false {
		t.Errorf("quitting a quiz = %v, want nothing recorded (same as quitting `sd quiz`)", fin)
	}
	p := hs.progress()
	if len(p.Module("T0").QuizAttempts) != 0 || len(p.Review.Cards) != 0 {
		t.Errorf("a quit quiz left records behind: %+v / %d cards", p.Module("T0").QuizAttempts, len(p.Review.Cards))
	}
	if c := hs.do("POST", "/api/sessions/"+sess.ID+"/answers", answerFor(sess.Items[1]), nil); c != http.StatusNotFound {
		t.Errorf("answering a finished session got %d, want 404", c)
	}
}

func TestReview_DueCardsAreServedAndRescheduled(t *testing.T) {
	hs := newHarness(t)
	takeQuiz(t, hs, 0, false) // two misses: box 0, due tomorrow

	var sess sessionView
	hs.do("POST", "/api/sessions", map[string]any{"kind": "review"}, &sess)
	if len(sess.Items) != 0 || sess.Empty == "" {
		t.Fatalf("nothing should be due today: %+v", sess)
	}
	hs.now = hs.now.Add(24 * time.Hour)
	hs.do("POST", "/api/sessions", map[string]any{"kind": "review"}, &sess)
	if len(sess.Items) != 2 {
		t.Fatalf("want 2 due cards tomorrow, got %d", len(sess.Items))
	}
	var ar answerResponse
	hs.do("POST", "/api/sessions/"+sess.ID+"/answers", answerFor(sess.Items[0]), &ar)
	p := hs.progress()
	if len(p.Review.Log) != 1 || p.Review.Cards[sess.Items[0].ID].Box != 1 {
		t.Errorf("a correct review should be saved immediately and promote the card: log=%d card=%+v", len(p.Review.Log), p.Review.Cards[sess.Items[0].ID])
	}
	var fin struct {
		Recorded bool            `json:"recorded"`
		Summary  *sessionSummary `json:"summary"`
	}
	hs.do("POST", "/api/sessions/"+sess.ID+"/finish", map[string]any{}, &fin)
	if !fin.Recorded || fin.Summary.Total != 1 {
		t.Errorf("ending a review early keeps what was answered: %+v", fin)
	}
	if last := hs.progress().Activity; last[len(last)-1].Kind != "review" {
		t.Errorf("review session should be logged, last activity = %+v", last[len(last)-1])
	}
}

func TestEstimate_SetRecordsAttemptsAtPolicyToleranceOnly(t *testing.T) {
	hs := newHarness(t)
	var set estimateSetView
	if c := hs.do("POST", "/api/estimate", map[string]any{"kind": "qps", "n": 2}, &set); c != 200 {
		t.Fatalf("start: %d", c)
	}
	if set.Problem.Prompt == "" || set.Problem.Kind != "qps" || !set.Counted {
		t.Fatalf("first problem = %+v", set)
	}
	var a estimateAnswerView
	if c := hs.do("POST", "/api/estimate/"+set.ID+"/answer", map[string]any{"input": "not a number"}, nil); c != http.StatusBadRequest {
		t.Errorf("unparseable answer got %d, want 400 so the learner can retry", c)
	}
	hs.do("POST", "/api/estimate/"+set.ID+"/answer", map[string]any{"input": "1"}, &a)
	if len(a.Result.Steps) == 0 || a.Result.Decision == "" || a.Next == nil || a.Done {
		t.Fatalf("answer should return a worked solution and the next problem: %+v", a)
	}
	hs.do("POST", "/api/estimate/"+set.ID+"/answer", map[string]any{"skip": true}, &a)
	if !a.Done || !a.Result.Skipped || a.Mastery == nil {
		t.Fatalf("second answer should finish the set: %+v", a)
	}
	p := hs.progress()
	if n := len(p.Estimate.Attempts); n != 2 {
		t.Errorf("recorded %d estimate attempts, want 2", n)
	}
	if p.Activity[len(p.Activity)-1].Kind != "estimate" {
		t.Error("a finished set should be logged as estimate activity")
	}

	hs.do("POST", "/api/estimate", map[string]any{"kind": "qps", "n": 1, "tol": 0.5}, &set)
	hs.do("POST", "/api/estimate/"+set.ID+"/answer", map[string]any{"skip": true}, &a)
	if n := len(hs.progress().Estimate.Attempts); n != 2 || set.Counted {
		t.Errorf("a non-default tolerance must not count toward mastery (attempts=%d counted=%v)", n, set.Counted)
	}
}

func TestLabs_RunTestsUnlocksSolutionButOnlyFullRunsGate(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test")
	}
	hs := newHarness(t)
	var e map[string]string
	if c := hs.do("GET", "/api/labs/T0/adder/solution", nil, &e); c != http.StatusForbidden {
		t.Fatalf("solution before any attempt got %d, want 403", c)
	}
	var labs labsResponse
	hs.do("GET", "/api/labs/T0", nil, &labs)
	if len(labs.Packages) != 1 || labs.Packages[0].SolutionUnlocked || labs.Readme != "labs/t0/README.md" {
		t.Fatalf("labs = %+v", labs)
	}

	var run runResponse
	if c := hs.do("POST", "/api/labs/T0/adder/run", map[string]any{"verbose": true}, &run); c != 200 {
		t.Fatalf("run: %d %+v", c, run)
	}
	if !run.Passed || !strings.Contains(run.Output, "TestAdd") {
		t.Fatalf("package run = passed %v, output:\n%s", run.Passed, run.Output)
	}
	if !run.Labs.Packages[0].SolutionUnlocked {
		t.Error("solution should unlock after the first run")
	}
	var sol solutionResponse
	if c := hs.do("GET", "/api/labs/T0/adder/solution", nil, &sol); c != 200 || len(sol.Files) != 2 || !strings.Contains(sol.Files[0].Content, "func Add") {
		t.Fatalf("solution = %d %+v", c, sol)
	}
	if sol.Files[1].Content != "" || !sol.Files[1].Test {
		t.Errorf("test files are listed, not repeated: %+v", sol.Files[1])
	}
	p := hs.progress()
	if len(p.Module("T0").LabAttempts) != 1 || len(p.Module("T0").LabRuns) != 0 {
		t.Fatalf("a single-package run is an attempt, not a gate run: %+v", p.Module("T0"))
	}

	if c := hs.do("POST", "/api/labs/T0/run", map[string]any{}, &run); c != 200 || !run.Passed {
		t.Fatalf("full run: %d passed=%v\n%s", c, run.Passed, run.Output)
	}
	if run.Labs.Gate == nil || !run.Labs.Gate.Met {
		t.Errorf("a passing full run should meet the lab component: %+v", run.Labs.Gate)
	}
	if c := hs.do("POST", "/api/labs/T0/nope/run", map[string]any{}, nil); c != http.StatusNotFound {
		t.Errorf("unknown package got %d, want 404", c)
	}
}

func TestProgress_DashboardPayload(t *testing.T) {
	hs := newHarness(t)
	takeQuiz(t, hs, 0, false)
	hs.do("PUT", "/api/predictions/T0-U1-P01", map[string]string{"answer": "four"}, nil)
	var pr progressResponse
	if c := hs.do("GET", "/api/progress", nil, &pr); c != 200 {
		t.Fatalf("progress: %d", c)
	}
	if len(pr.QuizHistory) != 1 || pr.QuizHistory[0].Passed {
		t.Errorf("quiz history = %+v, want one failed attempt", pr.QuizHistory)
	}
	if pr.Review.Total != 2 || len(pr.Review.Upcoming) != 2 || pr.Review.Due == nil {
		t.Errorf("review queue = %+v, want 2 upcoming cards", pr.Review)
	}
	if len(pr.Modules) != 1 || pr.Modules[0].ID != "T0" || pr.Modules[0].Predictions != 1 || pr.Modules[0].Designs == nil {
		t.Errorf("modules = %+v, want T0 with one prediction and empty (not null) lists", pr.Modules)
	}
	var mod moduleResponse
	hs.do("GET", "/api/modules/T0", nil, &mod)
	if len(mod.Docs) != 4 || mod.Quiz == nil || mod.Quiz.Full != 2 || mod.Quiz.Pool != 4 {
		t.Fatalf("module = %+v", mod)
	}
	for _, d := range mod.Docs {
		if d.Kind == "unit" && (d.Predicts != 1 || d.Answered != 1) {
			t.Errorf("unit doc predictions = %d/%d, want 1/1", d.Answered, d.Predicts)
		}
	}
	if len(mod.Dependents) != 1 || mod.Dependents[0] != "T1" {
		t.Errorf("dependents = %v, want [T1]", mod.Dependents)
	}
}
