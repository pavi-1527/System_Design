package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/chethu007/system-design-course/internal/estimate"
	"github.com/chethu007/system-design-course/internal/mastery"
	"github.com/chethu007/system-design-course/internal/progress"
)

// problemView is an estimation problem without its answer or worked solution.
type problemView struct {
	Index  int    `json:"index"`
	Kind   string `json:"kind"`
	Prompt string `json:"prompt"`
	Unit   string `json:"unit"`
}

type estimateStart struct {
	Kind string  `json:"kind"`
	N    int     `json:"n"`
	Tol  float64 `json:"tol"` // 0 = policy tolerance; only that one counts toward mastery
}

type estimateSetView struct {
	ID        string                 `json:"id"`
	Total     int                    `json:"total"`
	Tolerance float64                `json:"tolerance"`
	Counted   bool                   `json:"counted"`
	Kinds     []string               `json:"kinds"`
	Problem   problemView            `json:"problem"`
	Mastery   mastery.EstimateStatus `json:"mastery"`
}

type estimateResult struct {
	Within   bool     `json:"within"`
	Skipped  bool     `json:"skipped"`
	Given    string   `json:"given,omitempty"`
	Expected string   `json:"expected"`
	Ratio    float64  `json:"ratio,omitempty"`
	Note     string   `json:"note,omitempty"`
	Hint     string   `json:"hint,omitempty"`
	Steps    []string `json:"steps"`
	Decision string   `json:"decision"`
}

type estimateAnswer struct {
	Input string `json:"input"`
	Skip  bool   `json:"skip"`
}

type estimateAnswerView struct {
	Result  estimateResult          `json:"result"`
	Next    *problemView            `json:"next,omitempty"`
	Done    bool                    `json:"done"`
	Within  int                     `json:"within"`
	Count   int                     `json:"count"`
	Mastery *mastery.EstimateStatus `json:"mastery,omitempty"`
}

// handleEstimateStart generates a set of n problems (kind "" or "any" mixes kinds) and
// returns the first one.
func (s *Server) handleEstimateStart(w http.ResponseWriter, r *http.Request) {
	var req estimateStart
	if err := decode(r, &req); err != nil {
		reply(w, nil, err)
		return
	}
	if req.N <= 0 {
		req.N = 5
	}
	if req.N > 50 {
		req.N = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load()
	if err != nil {
		reply(w, nil, err)
		return
	}
	pol := st.c.Policy
	sess := &session{kind: kindEstimate, tol: pol.EstimateTolerance}
	if req.Tol > 0 && req.Tol <= 1 {
		sess.tol = req.Tol
	}
	for i := 0; i < req.N; i++ {
		p, err := estimate.Generate(strings.TrimSpace(req.Kind), s.rng)
		if err != nil {
			reply(w, nil, errorf(http.StatusBadRequest, "%v", err))
			return
		}
		sess.problems = append(sess.problems, p)
	}
	sess.estimate = make([]*estimateResult, req.N)
	s.addSession(sess)
	reply(w, estimateSetView{
		ID: sess.id, Total: req.N, Tolerance: sess.tol, Counted: sess.tol == pol.EstimateTolerance,
		Kinds: estimate.Kinds(), Problem: problemOf(sess, 0), Mastery: mastery.EstimateSummary(pol, st.p),
	}, nil)
}

func problemOf(sess *session, i int) problemView {
	p := sess.problems[i]
	return problemView{Index: i, Kind: p.Kind, Prompt: p.Prompt, Unit: p.Unit}
}

// handleEstimateAnswer checks the current problem, records it (at the policy tolerance
// only, as `sd estimate` does) and returns the worked solution plus the next problem.
func (s *Server) handleEstimateAnswer(w http.ResponseWriter, r *http.Request) {
	var req estimateAnswer
	if err := decode(r, &req); err != nil {
		reply(w, nil, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.getSession(r.PathValue("sid"), kindEstimate)
	if err != nil {
		reply(w, nil, err)
		return
	}
	i := 0
	for i < len(sess.estimate) && sess.estimate[i] != nil {
		i++
	}
	if sess.done || i == len(sess.problems) {
		reply(w, nil, errorf(http.StatusConflict, "this set is finished"))
		return
	}
	p := sess.problems[i]
	res := estimateResult{Expected: estimate.Show(p.Answer, p.Unit), Steps: p.Steps, Decision: p.Decision}
	var att progress.EstimateAttempt
	if req.Skip {
		res.Skipped = true
		att = progress.EstimateAttempt{Kind: p.Kind, Expected: p.Answer}
	} else {
		chk, err := estimate.Check(p, req.Input, sess.tol)
		if err != nil {
			reply(w, nil, errorf(http.StatusBadRequest, "%v", err))
			return
		}
		res.Within, res.Ratio, res.Note = chk.Within, chk.Ratio, chk.Note
		res.Given = estimate.Show(chk.Given, p.Unit)
		if !chk.Within && chk.Ratio > 0 {
			res.Hint = estimate.Diagnose(chk.Ratio)
		}
		att = progress.EstimateAttempt{Kind: p.Kind, Expected: p.Answer, Given: chk.Given, Ratio: chk.Ratio, Within: chk.Within}
	}
	sess.estimate[i] = &res
	out := estimateAnswerView{Result: res}
	for _, e := range sess.estimate {
		if e != nil {
			out.Count++
			if e.Within {
				out.Within++
			}
		}
	}
	out.Done = out.Count == len(sess.problems)
	if !out.Done {
		nx := problemOf(sess, i+1)
		out.Next = &nx
	}
	err = s.mutate(func(st *state) error {
		now := s.now()
		pol := st.c.Policy
		if sess.tol == pol.EstimateTolerance {
			att.At = now
			st.p.Estimate.Attempts = append(st.p.Estimate.Attempts, att)
		}
		if out.Done {
			sess.done = true
			delete(s.sessions, sess.id)
			st.p.Touch(now, "estimate", "", fmt.Sprintf("%d/%d within tolerance", out.Within, out.Count))
		}
		es := mastery.EstimateSummary(pol, st.p)
		out.Mastery = &es
		return nil
	})
	reply(w, out, err)
}
