package web

import (
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/chethu007/system-design-course/internal/course"
	"github.com/chethu007/system-design-course/internal/lesson"
	"github.com/chethu007/system-design-course/internal/mastery"
	"github.com/chethu007/system-design-course/internal/progress"
	"github.com/chethu007/system-design-course/internal/quiz"
)

// roadmapModule is one module on the roadmap: its evaluated status plus the metadata a
// card needs.
type roadmapModule struct {
	mastery.ModuleStatus
	Summary       string   `json:"summary"`
	Prerequisites []string `json:"prerequisites"`
	Readme        string   `json:"readme,omitempty"`
	Units         int      `json:"units"`
	Labs          int      `json:"labs"`
	Order         int      `json:"order"` // position in recommended_order
}

type statusResponse struct {
	Title        string          `json:"title"`
	Tracks       []course.Track  `json:"tracks"`
	Policy       course.Policy   `json:"policy"`
	Report       *mastery.Report `json:"report"`
	Modules      []roadmapModule `json:"modules"`
	LastActiveAt *time.Time      `json:"last_active_at,omitempty"`
	Learner      string          `json:"learner,omitempty"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.load()
	if err != nil {
		reply(w, nil, err)
		return
	}
	rep := mastery.Evaluate(st.c, st.p, s.now())
	resp := statusResponse{
		Title: st.c.Title, Tracks: st.c.Tracks, Policy: st.c.Policy, Report: rep,
		LastActiveAt: st.p.LastActiveAt, Learner: st.p.Learner.Name,
	}
	for i, ms := range rep.Modules {
		m := st.c.Modules[ms.ID]
		rm := roadmapModule{ModuleStatus: ms, Summary: m.Summary, Prerequisites: m.Prerequisites, Units: len(m.Units), Labs: len(m.Labs), Order: i}
		if m.Written() {
			rm.Readme = m.Paths.Readme
		}
		if rm.Prerequisites == nil {
			rm.Prerequisites = []string{}
		}
		resp.Modules = append(resp.Modules, rm)
	}
	reply(w, resp, nil)
}

// docRef is a lesson-like file of a module with its predict-block progress.
type docRef struct {
	Kind      string  `json:"kind"` // readme, unit, design, teachback
	N         int     `json:"n,omitempty"`
	Title     string  `json:"title"`
	Path      string  `json:"path"`
	Core      float64 `json:"core_hours,omitempty"`
	Stretch   float64 `json:"stretch_hours,omitempty"`
	Predicts  int     `json:"predicts"`
	Answered  int     `json:"answered"`
	Available bool    `json:"available"`
}

type moduleResponse struct {
	Module     *course.Module         `json:"module"`
	Status     mastery.ModuleStatus   `json:"status"`
	Docs       []docRef               `json:"docs"`
	Quiz       *quizInfo              `json:"quiz,omitempty"`
	Attempts   []progress.QuizAttempt `json:"quiz_attempts"`
	Dependents []string               `json:"dependents"`
}

type quizInfo struct {
	Title string `json:"title"`
	Pool  int    `json:"pool"`
	Full  int    `json:"full"`
	Error string `json:"error,omitempty"`
}

func (s *Server) handleModule(w http.ResponseWriter, r *http.Request) {
	st, err := s.load()
	if err != nil {
		reply(w, nil, err)
		return
	}
	m, err := moduleOf(st.c, r.PathValue("id"))
	if err != nil {
		reply(w, nil, err)
		return
	}
	resp := moduleResponse{Module: m, Status: mastery.Status(st.c, m, st.p, s.now()), Dependents: []string{}}
	rec := st.p.Peek(m.ID)
	if rec != nil {
		resp.Attempts = rec.QuizAttempts
	}
	if resp.Attempts == nil {
		resp.Attempts = []progress.QuizAttempt{}
	}
	if m.Written() {
		add := func(d docRef) {
			b, err := os.ReadFile(st.c.Path(d.Path))
			if err == nil {
				d.Available = true
				_, ids := lesson.Prepare(string(b))
				d.Predicts = len(ids)
				for _, id := range ids {
					if rec != nil && rec.Predictions[id] != nil {
						d.Answered++
					}
				}
			}
			resp.Docs = append(resp.Docs, d)
		}
		if m.Paths.Readme != "" {
			add(docRef{Kind: "readme", Title: "Overview", Path: m.Paths.Readme})
		}
		for _, u := range m.Units {
			add(docRef{Kind: "unit", N: u.N, Title: u.Title, Path: u.File, Core: u.Hours.Core, Stretch: u.Hours.Stretch})
		}
		if m.Paths.DesignExercise != "" {
			add(docRef{Kind: "design", Title: "Design exercise", Path: m.Paths.DesignExercise})
		}
		if m.Paths.Teachback != "" {
			add(docRef{Kind: "teachback", Title: "Teach-back", Path: m.Paths.Teachback})
		}
		if m.Paths.Quiz != "" {
			qi := &quizInfo{}
			if qz, err := st.c.LoadQuiz(m); err != nil {
				qi.Error = err.Error()
			} else {
				qi.Title, qi.Pool = qz.Title, len(qz.Questions)
				qi.Full = quiz.FullLength(qz, st.c.Policy.DefaultQuizDraw)
			}
			resp.Quiz = qi
		}
	}
	for _, o := range st.c.Ordered() {
		for _, pre := range o.Prerequisites {
			if pre == m.ID {
				resp.Dependents = append(resp.Dependents, o.ID)
			}
		}
	}
	reply(w, resp, nil)
}

type docResponse struct {
	Path        string                          `json:"path"`
	Module      string                          `json:"module,omitempty"`
	ModuleTitle string                          `json:"module_title,omitempty"`
	Markdown    string                          `json:"markdown"`
	PredictIDs  []string                        `json:"predict_ids"`
	Predictions map[string]*progress.Prediction `json:"predictions"`
}

// cleanDocPath accepts a repo-relative Markdown path and refuses anything that could
// leave the course root.
func cleanDocPath(p string) (string, error) {
	p = strings.TrimPrefix(p, "/")
	if p == "" || strings.Contains(p, "\\") || strings.ContainsRune(p, 0) {
		return "", errorf(http.StatusBadRequest, "bad path")
	}
	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return "", errorf(http.StatusBadRequest, "path leaves the course root")
	}
	if !strings.EqualFold(path.Ext(clean), ".md") {
		return "", errorf(http.StatusBadRequest, "only Markdown files are served")
	}
	for _, part := range strings.Split(clean, "/") {
		if strings.HasPrefix(part, ".") || part == "node_modules" {
			return "", errorf(http.StatusBadRequest, "hidden paths are not served")
		}
	}
	return clean, nil
}

// handleDoc serves a Markdown file (a lesson, a lab README, docs/…) prepared for the
// browser, with the learner's saved predictions for its predict blocks.
func (s *Server) handleDoc(w http.ResponseWriter, r *http.Request) {
	p, err := cleanDocPath(r.URL.Query().Get("path"))
	if err != nil {
		reply(w, nil, err)
		return
	}
	st, err := s.load()
	if err != nil {
		reply(w, nil, err)
		return
	}
	b, err := os.ReadFile(st.c.Path(p))
	if err != nil {
		reply(w, nil, errorf(http.StatusNotFound, "%s not found", p))
		return
	}
	md, ids := lesson.Prepare(string(b))
	resp := docResponse{Path: p, Markdown: md, PredictIDs: ids, Predictions: map[string]*progress.Prediction{}}
	if ids == nil {
		resp.PredictIDs = []string{}
	}
	if m := moduleForPath(st.c, p); m != nil {
		resp.Module, resp.ModuleTitle = m.ID, m.Title
		if rec := st.p.Peek(m.ID); rec != nil {
			for _, id := range ids {
				if pr := rec.Predictions[id]; pr != nil {
					resp.Predictions[id] = pr
				}
			}
		}
	}
	reply(w, resp, nil)
}

// moduleForPath finds the module whose directory (or lab directory) contains p.
func moduleForPath(c *course.Course, p string) *course.Module {
	for _, m := range c.Ordered() {
		for _, dir := range []string{m.Paths.Dir, m.Paths.Lab} {
			if dir != "" && strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/") {
				return m
			}
		}
	}
	return nil
}

var predictIDRe = regexp.MustCompile(`^([A-Z][0-9]+)-U[0-9]+-P[0-9]+$`)

type predictRequest struct {
	Answer string `json:"answer"`
}

type predictResponse struct {
	ID         string              `json:"id"`
	Prediction progress.Prediction `json:"prediction"`
	Created    bool                `json:"created"`
}

// handlePredict saves the learner's answer to a predict block; the reveal opens only
// after this succeeds. A prediction can't be changed once saved.
func (s *Server) handlePredict(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	mm := predictIDRe.FindStringSubmatch(pid)
	if mm == nil {
		reply(w, nil, errorf(http.StatusBadRequest, "bad predict id %q", pid))
		return
	}
	var req predictRequest
	if err := decode(r, &req); err != nil {
		reply(w, nil, err)
		return
	}
	req.Answer = strings.TrimSpace(req.Answer)
	if req.Answer == "" {
		reply(w, nil, errorf(http.StatusBadRequest, "write your prediction before revealing the answer"))
		return
	}
	if len(req.Answer) > 20000 {
		reply(w, nil, errorf(http.StatusBadRequest, "prediction too long (max 20,000 characters)"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load()
	if err != nil {
		reply(w, nil, err)
		return
	}
	m, err := moduleOf(st.c, mm[1])
	if err != nil {
		reply(w, nil, err)
		return
	}
	pred, created := st.p.Predict(m.ID, pid, req.Answer, s.now())
	if created {
		if err := s.save(st); err != nil {
			reply(w, nil, err)
			return
		}
	}
	reply(w, predictResponse{ID: pid, Prediction: pred, Created: created}, nil)
}

type moduleHistory struct {
	ID           string                `json:"id"`
	Title        string                `json:"title"`
	State        mastery.State         `json:"state"`
	Components   []mastery.Component   `json:"components"`
	GatePassedAt *time.Time            `json:"gate_passed_at,omitempty"`
	Refresh      string                `json:"refresh_reason,omitempty"`
	QuizAttempts []quizAttemptSummary  `json:"quiz_attempts"`
	LabRuns      []progress.LabRun     `json:"lab_runs"`
	LabAttempts  []progress.LabAttempt `json:"lab_attempts"`
	Designs      []progress.Assessment `json:"designs"`
	Teachbacks   []progress.Assessment `json:"teachbacks"`
	Mocks        []progress.Mock       `json:"mocks"`
	Predictions  int                   `json:"predictions"`
}

type quizAttemptSummary struct {
	At      time.Time `json:"at"`
	Module  string    `json:"module"`
	Correct int       `json:"correct"`
	Total   int       `json:"total"`
	Score   float64   `json:"score"`
	Passed  bool      `json:"passed"`
}

type reviewCard struct {
	QID    string   `json:"qid"`
	Module string   `json:"module"`
	Box    int      `json:"box"`
	Due    string   `json:"due"`
	Reps   int      `json:"reps"`
	Lapses int      `json:"lapses"`
	Prompt string   `json:"prompt"`
	Tags   []string `json:"tags"`
}

type reviewSummary struct {
	Total     int          `json:"total"`
	DueToday  int          `json:"due_today"`
	Lapses    int          `json:"lapses"`
	Intervals []int        `json:"intervals"`
	Boxes     []int        `json:"boxes"`
	Due       []reviewCard `json:"due"`
	Upcoming  []reviewCard `json:"upcoming"`
	Accuracy  float64      `json:"accuracy_recent"`
	Recent    int          `json:"recent"`
}

type progressResponse struct {
	Report         *mastery.Report      `json:"report"`
	Policy         course.Policy        `json:"policy"`
	StartedAt      *time.Time           `json:"started_at,omitempty"`
	Modules        []moduleHistory      `json:"modules"`
	QuizHistory    []quizAttemptSummary `json:"quiz_history"`
	Review         reviewSummary        `json:"review"`
	WeakTags       []mastery.TagStat    `json:"weak_tags"`
	EstimateKinds  []string             `json:"estimate_kinds"`
	EstimateByKind map[string][2]int    `json:"estimate_by_kind"`
	TutorNotes     []progress.TutorNote `json:"tutor_notes"`
	Activity       []progress.Activity  `json:"activity"`
}

func (s *Server) handleProgress(w http.ResponseWriter, r *http.Request) {
	st, err := s.load()
	if err != nil {
		reply(w, nil, err)
		return
	}
	now := s.now()
	c, p := st.c, st.p
	idx := c.QuestionIndex()
	rep := mastery.Evaluate(c, p, now)
	resp := progressResponse{
		Report: rep, Policy: c.Policy, StartedAt: p.Learner.StartedAt,
		WeakTags:       mastery.WeakTags(p, idx, 20, 3),
		EstimateKinds:  mastery.EstimateKinds,
		EstimateByKind: mastery.EstimateByKind(p),
		Modules:        []moduleHistory{},
		QuizHistory:    []quizAttemptSummary{},
	}
	if resp.WeakTags == nil {
		resp.WeakTags = []mastery.TagStat{}
	}
	for _, ms := range rep.Modules {
		rec := p.Peek(ms.ID)
		if !rec.HasActivity() && ms.GatePassedAt == nil {
			continue
		}
		h := moduleHistory{
			ID: ms.ID, Title: ms.Title, State: ms.State, Components: ms.Components,
			GatePassedAt: ms.GatePassedAt, Refresh: ms.RefreshReason,
			LabRuns: orEmpty(rec.LabRuns), LabAttempts: orEmpty(rec.LabAttempts), Designs: orEmpty(rec.Designs),
			Teachbacks: orEmpty(rec.Teachbacks), Mocks: orEmpty(rec.Mocks), Predictions: len(rec.Predictions),
			QuizAttempts: []quizAttemptSummary{},
		}
		for _, a := range rec.QuizAttempts {
			qa := quizAttemptSummary{At: a.At, Module: ms.ID, Correct: a.Correct, Total: a.Total, Score: a.Score,
				Passed: a.Score+1e-9 >= c.Policy.QuizPassThreshold}
			h.QuizAttempts = append(h.QuizAttempts, qa)
			resp.QuizHistory = append(resp.QuizHistory, qa)
		}
		resp.Modules = append(resp.Modules, h)
	}
	sort.SliceStable(resp.QuizHistory, func(i, j int) bool { return resp.QuizHistory[i].At.Before(resp.QuizHistory[j].At) })

	rv := reviewSummary{Intervals: c.Policy.ReviewIntervalsDays, Boxes: make([]int, len(c.Policy.ReviewIntervalsDays)),
		Due: []reviewCard{}, Upcoming: []reviewCard{}}
	today := progress.Date(now)
	var cards []reviewCard
	for qid, card := range p.Review.Cards {
		q := idx[qid]
		if q == nil {
			continue // question retired from its quiz
		}
		rv.Total++
		rv.Lapses += card.Lapses
		if card.Box >= 0 && card.Box < len(rv.Boxes) {
			rv.Boxes[card.Box]++
		}
		cards = append(cards, reviewCard{QID: qid, Module: card.Module, Box: card.Box, Due: card.Due, Reps: card.Reps,
			Lapses: card.Lapses, Prompt: firstLine(q.Prompt, 160), Tags: q.Tags})
	}
	sort.Slice(cards, func(i, j int) bool {
		if cards[i].Due != cards[j].Due {
			return cards[i].Due < cards[j].Due
		}
		return cards[i].QID < cards[j].QID
	})
	for _, cd := range cards {
		if cd.Due <= today {
			rv.DueToday++
			if len(rv.Due) < 50 {
				rv.Due = append(rv.Due, cd)
			}
		} else if len(rv.Upcoming) < 30 {
			rv.Upcoming = append(rv.Upcoming, cd)
		}
	}
	log := p.Review.Log
	if n := len(log); n > 50 {
		log = log[n-50:]
	}
	ok := 0
	for _, e := range log {
		if e.Correct {
			ok++
		}
	}
	rv.Recent = len(log)
	if len(log) > 0 {
		rv.Accuracy = float64(ok) / float64(len(log))
	}
	resp.Review = rv

	resp.TutorNotes = lastN(p.TutorNotes, 10)
	resp.Activity = lastN(p.Activity, 25)
	reply(w, resp, nil)
}

// orEmpty keeps JSON arrays as [] rather than null for the front end.
func orEmpty[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}

func lastN[T any](xs []T, n int) []T {
	if len(xs) > n {
		xs = xs[len(xs)-n:]
	}
	out := make([]T, len(xs))
	copy(out, xs)
	// newest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func firstLine(s string, max int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	if r := []rune(s); len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}
