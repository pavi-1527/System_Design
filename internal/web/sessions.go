package web

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chethu007/system-design-course/internal/course"
	"github.com/chethu007/system-design-course/internal/estimate"
	"github.com/chethu007/system-design-course/internal/mastery"
	"github.com/chethu007/system-design-course/internal/progress"
	"github.com/chethu007/system-design-course/internal/quiz"
	"github.com/chethu007/system-design-course/internal/review"
)

// Session kinds. A quiz records nothing until its last answer (like quitting `sd quiz`);
// review and recap answers are saved one by one (like `sd review`).
const (
	kindQuiz     = "quiz"
	kindReview   = "review"
	kindRecap    = "recap"
	kindEstimate = "estimate"
)

const sessionTTL = 12 * time.Hour

type session struct {
	id      string
	kind    string
	module  string // quiz only
	gated   bool   // quiz only: full-length attempt
	created time.Time
	done    bool

	items   []quiz.Item
	dirs    map[string]string // module id → module directory, for question refs
	results []*quiz.Result

	problems []estimate.Problem
	estimate []*estimateResult
	tol      float64
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// addSession stores sess and drops abandoned ones. Callers hold s.mu.
func (s *Server) addSession(sess *session) {
	for id, old := range s.sessions {
		if time.Since(old.created) > sessionTTL {
			delete(s.sessions, id)
		}
	}
	sess.id = newID()
	sess.created = time.Now()
	s.sessions[sess.id] = sess
}

func (s *Server) getSession(id, kind string) (*session, error) {
	sess := s.sessions[id]
	if sess == nil || (kind != "" && sess.kind != kind) || (kind == "" && sess.kind == kindEstimate) {
		return nil, errorf(http.StatusNotFound, "session not found (it may have expired or the server restarted); start a new one")
	}
	return sess, nil
}

// itemView is a question as shown to the learner: no answer, choices in display order.
type itemView struct {
	Index      int      `json:"index"`
	ID         string   `json:"id"`
	Module     string   `json:"module"`
	Type       string   `json:"type"`
	Tags       []string `json:"tags"`
	Difficulty int      `json:"difficulty,omitempty"`
	Prompt     string   `json:"prompt"`
	Choices    []string `json:"choices,omitempty"`
	Unit       string   `json:"unit,omitempty"`
}

type resultView struct {
	quiz.Result
	CorrectChoices []int  `json:"correct_choices,omitempty"` // 1-based display positions
	RefPath        string `json:"ref_path,omitempty"`        // repo-relative file of Ref
	RefAnchor      string `json:"ref_anchor,omitempty"`
}

type sessionView struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Module    string     `json:"module,omitempty"`
	Title     string     `json:"title"`
	Gated     bool       `json:"gated"`
	Full      int        `json:"full,omitempty"`
	Threshold float64    `json:"threshold"`
	Locked    []string   `json:"locked_by,omitempty"`
	Items     []itemView `json:"items"`
	Empty     string     `json:"empty,omitempty"` // why there is nothing to do
}

type startRequest struct {
	Kind   string `json:"kind"`
	Module string `json:"module"`
	N      int    `json:"n"`
}

func (s *Server) handleSessionStart(w http.ResponseWriter, r *http.Request) {
	var req startRequest
	if err := decode(r, &req); err != nil {
		reply(w, nil, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load()
	if err != nil {
		reply(w, nil, err)
		return
	}
	now := s.now()
	sess := &session{kind: req.Kind, dirs: map[string]string{}}
	for id, m := range st.c.Modules {
		sess.dirs[id] = m.Paths.Dir
	}
	view := sessionView{Kind: req.Kind, Threshold: st.c.Policy.QuizPassThreshold}
	switch req.Kind {
	case kindQuiz:
		m, err := moduleOf(st.c, req.Module)
		if err != nil {
			reply(w, nil, err)
			return
		}
		qz, err := st.c.LoadQuiz(m)
		if err != nil {
			reply(w, nil, errorf(http.StatusBadRequest, "%v", err))
			return
		}
		full := quiz.FullLength(qz, st.c.Policy.DefaultQuizDraw)
		draw := full
		if req.N > 0 {
			draw = req.N
		}
		sess.module, sess.gated = m.ID, draw >= full
		sess.items = quiz.Draw(qz, draw, quiz.Seen(st.p.Peek(m.ID)), s.rng)
		for i := range sess.items {
			sess.items[i].Q.Module = m.ID
		}
		view.Module, view.Title, view.Gated, view.Full = m.ID, m.ID+" quiz — "+m.Title, sess.gated, full
		if ok, missing := mastery.Unlocked(st.c, m, st.p); !ok {
			view.Locked = missing
		}
	case kindReview:
		idx := st.c.QuestionIndex()
		n := req.N
		if n <= 0 {
			n = st.c.Policy.ReviewSessionSize
		}
		for _, qid := range review.Due(st.p, idx, now, n, s.rng) {
			sess.items = append(sess.items, quiz.Prepare(idx[qid], s.rng))
		}
		view.Title = "Spaced review"
		if len(sess.items) == 0 {
			if rep := mastery.Evaluate(st.c, st.p, now); rep.NextReviewDue != "" {
				view.Empty = "Nothing due today. The next card is due " + rep.NextReviewDue + "."
			} else {
				view.Empty = "Your review deck is empty. Questions join it when you take a quiz."
			}
		}
	case kindRecap:
		idx := st.c.QuestionIndex()
		for _, qid := range review.Recap(st.p, idx, 5, s.rng) {
			sess.items = append(sess.items, quiz.Prepare(idx[qid], s.rng))
		}
		view.Title = "Recap warm-up"
		if len(sess.items) == 0 {
			view.Empty = "Nothing to warm up on yet: take a quiz first."
		}
	default:
		reply(w, nil, errorf(http.StatusBadRequest, "kind must be quiz, review or recap"))
		return
	}
	sess.results = make([]*quiz.Result, len(sess.items))
	view.Items = []itemView{}
	if len(sess.items) > 0 {
		s.addSession(sess)
		view.ID = sess.id
	}
	for i, it := range sess.items {
		view.Items = append(view.Items, itemView{
			Index: i, ID: it.Q.ID, Module: it.Q.Module, Type: it.Q.Type, Tags: it.Q.Tags, Difficulty: it.Q.Difficulty,
			Prompt: it.Q.Prompt, Choices: it.Choices(), Unit: it.Q.Unit,
		})
	}
	reply(w, view, nil)
}

type answerRequest struct {
	Index   int    `json:"index"`
	Choices []int  `json:"choices"` // 1-based display positions (single/multi)
	Input   string `json:"input"`   // numeric answer, e.g. "1.2k" or "40 TB"
	Skip    bool   `json:"skip"`
}

type answerResponse struct {
	Result  resultView      `json:"result"`
	Done    bool            `json:"done"`
	Summary *sessionSummary `json:"summary,omitempty"`
}

type sessionSummary struct {
	Kind       string              `json:"kind"`
	Module     string              `json:"module,omitempty"`
	Correct    int                 `json:"correct"`
	Total      int                 `json:"total"`
	Score      float64             `json:"score"`
	Gated      bool                `json:"gated"`
	Pass       bool                `json:"pass"`
	MissedTags []tagCount          `json:"missed_tags"`
	Gate       []mastery.Component `json:"gate,omitempty"`
	Passed     []string            `json:"gates_passed"`
	Refresh    []string            `json:"needs_refresh"`
}

type tagCount struct {
	Tag string `json:"tag"`
	N   int    `json:"n"`
}

func (s *Server) handleSessionAnswer(w http.ResponseWriter, r *http.Request) {
	var req answerRequest
	if err := decode(r, &req); err != nil {
		reply(w, nil, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.getSession(r.PathValue("sid"), "")
	if err != nil {
		reply(w, nil, err)
		return
	}
	if sess.done {
		reply(w, nil, errorf(http.StatusConflict, "this session is finished"))
		return
	}
	if req.Index < 0 || req.Index >= len(sess.items) {
		reply(w, nil, errorf(http.StatusBadRequest, "no question %d", req.Index))
		return
	}
	if sess.results[req.Index] != nil {
		reply(w, nil, errorf(http.StatusConflict, "question %d is already answered", req.Index+1))
		return
	}
	it := sess.items[req.Index]
	var res quiz.Result
	switch {
	case req.Skip:
		res = it.Skip()
	case it.Q.Type == course.QNumeric:
		res, err = it.GradeNumeric(req.Input)
	default:
		res, err = it.Grade(joinInts(req.Choices))
	}
	if err != nil {
		reply(w, nil, errorf(http.StatusBadRequest, "%v", err))
		return
	}
	sess.results[req.Index] = &res

	resp := answerResponse{Result: view(sess.dirs[it.Q.Module], it, res)}
	// Review and recap answers stand alone, so they are saved immediately.
	if sess.kind == kindReview || (sess.kind == kindRecap && !res.Correct) {
		if err := s.mutate(func(st *state) error {
			review.Answer(st.p, st.c.Policy, it.Q.ID, res.Correct, s.now())
			return nil
		}); err != nil {
			reply(w, nil, err)
			return
		}
	}
	if answered(sess) == len(sess.items) {
		sum, err := s.finish(sess)
		if err != nil {
			reply(w, nil, err)
			return
		}
		resp.Done, resp.Summary = true, sum
	}
	reply(w, resp, nil)
}

// handleSessionFinish ends a session early. A quiz ended early records nothing; review
// and recap keep the answers already given and log the session.
func (s *Server) handleSessionFinish(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.getSession(r.PathValue("sid"), "")
	if err != nil {
		reply(w, nil, err)
		return
	}
	if sess.done {
		reply(w, map[string]any{"done": true}, nil)
		return
	}
	if sess.kind == kindQuiz {
		sess.done = true
		delete(s.sessions, sess.id)
		reply(w, map[string]any{"done": true, "recorded": false}, nil)
		return
	}
	sum, err := s.finish(sess)
	reply(w, map[string]any{"done": true, "recorded": sum != nil, "summary": sum}, err)
}

// finish records a completed (or, for review/recap, ended) session. Callers hold s.mu.
func (s *Server) finish(sess *session) (*sessionSummary, error) {
	sess.done = true
	sum := &sessionSummary{Kind: sess.kind, Module: sess.module, Gated: sess.gated, MissedTags: []tagCount{}, Passed: []string{}, Refresh: []string{}}
	var answers []progress.AnswerRecord
	miss := map[string]int{}
	for i, res := range sess.results {
		if res == nil {
			continue
		}
		sum.Total++
		if res.Correct {
			sum.Correct++
		} else {
			for _, t := range sess.items[i].Q.Tags {
				miss[t]++
			}
		}
		answers = append(answers, progress.AnswerRecord{QID: sess.items[i].Q.ID, Correct: res.Correct, Given: res.Given})
	}
	sum.Score = quiz.Score(sum.Correct, sum.Total)
	for t, n := range miss {
		sum.MissedTags = append(sum.MissedTags, tagCount{t, n})
	}
	sort.Slice(sum.MissedTags, func(i, j int) bool {
		a, b := sum.MissedTags[i], sum.MissedTags[j]
		return a.N > b.N || (a.N == b.N && a.Tag < b.Tag)
	})
	if sum.Total == 0 {
		delete(s.sessions, sess.id)
		return nil, nil
	}
	err := s.mutate(func(st *state) error {
		now := s.now()
		switch sess.kind {
		case kindQuiz:
			score, passed := mastery.CompleteQuiz(st.c, st.p, sess.module, answers, sess.gated, now)
			sum.Score = score
			sum.Pass = sess.gated && score+1e-9 >= st.c.Policy.QuizPassThreshold
			sum.Passed = append(sum.Passed, passed...)
			if m, err := st.c.Module(sess.module); err == nil {
				sum.Gate = mastery.Gate(st.c, m, st.p)
			}
		case kindReview:
			st.p.Touch(now, "review", "", fmt.Sprintf("%d/%d", sum.Correct, sum.Total))
			sum.Refresh = append(sum.Refresh, mastery.CheckRefresh(st.c, st.p, now)...)
		case kindRecap:
			st.p.Touch(now, "recap", "", fmt.Sprintf("%d/%d", sum.Correct, sum.Total))
		}
		return nil
	})
	delete(s.sessions, sess.id)
	return sum, err
}

// mutate loads fresh state, applies f and saves. Callers hold s.mu.
func (s *Server) mutate(f func(*state) error) error {
	st, err := s.load()
	if err != nil {
		return err
	}
	if err := f(st); err != nil {
		return err
	}
	return s.save(st)
}

func answered(sess *session) int {
	n := 0
	for _, r := range sess.results {
		if r != nil {
			n++
		}
	}
	return n
}

func view(dir string, it quiz.Item, res quiz.Result) resultView {
	v := resultView{Result: res}
	if it.Q.Type != course.QNumeric {
		v.CorrectChoices = it.CorrectDisplay()
	}
	v.RefPath, v.RefAnchor = refLink(dir, it.Q)
	return v
}

// refLink resolves a question's module-relative ref ("unit-2-x.md#2-pagination") to a
// repo-relative path and an anchor.
func refLink(dir string, q *course.Question) (string, string) {
	if q.Ref == "" || dir == "" {
		return "", ""
	}
	file, anchor, _ := strings.Cut(q.Ref, "#")
	return path.Join(dir, file), anchor
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(x)
	}
	return strings.Join(parts, ",")
}
