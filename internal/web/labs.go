package web

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/chethu007/system-design-course/internal/course"
	"github.com/chethu007/system-design-course/internal/labrun"
	"github.com/chethu007/system-design-course/internal/mastery"
	"github.com/chethu007/system-design-course/internal/progress"
)

type labPackage struct {
	course.Lab
	Dir              string               `json:"dir"`
	Files            []string             `json:"files"`
	Attempted        bool                 `json:"attempted"`
	SolutionUnlocked bool                 `json:"solution_unlocked"`
	HasSolution      bool                 `json:"has_solution"`
	Last             *progress.LabAttempt `json:"last,omitempty"`
}

type labsResponse struct {
	Module        string             `json:"module"`
	Title         string             `json:"title"`
	Written       bool               `json:"written"`
	Dir           string             `json:"dir"`
	Readme        string             `json:"readme,omitempty"`
	RaceAvailable bool               `json:"race_available"`
	Packages      []labPackage       `json:"packages"`
	LastRun       *progress.LabRun   `json:"last_run,omitempty"`
	Gate          *mastery.Component `json:"gate,omitempty"`
}

func (s *Server) handleLabs(w http.ResponseWriter, r *http.Request) {
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
	reply(w, s.labsView(r.Context(), st, m), nil)
}

func (s *Server) labsView(ctx context.Context, st *state, m *course.Module) labsResponse {
	resp := labsResponse{Module: m.ID, Title: m.Title, Written: m.Written(), Dir: m.Paths.Lab,
		RaceAvailable: s.raceAvailable(ctx), Packages: []labPackage{}}
	if m.Paths.Lab != "" {
		if _, err := os.Stat(st.c.Path(m.Paths.Lab + "/README.md")); err == nil {
			resp.Readme = m.Paths.Lab + "/README.md"
		}
	}
	rec := st.p.Peek(m.ID)
	for _, l := range m.Labs {
		lp := labPackage{Lab: l, Dir: path.Join(m.Paths.Lab, l.Package), Files: goFiles(st.c, path.Join(m.Paths.Lab, l.Package), true)}
		lp.Attempted = rec.Attempted(l.Package)
		lp.HasSolution = m.Paths.Solution != "" && len(goFiles(st.c, path.Join(m.Paths.Solution, l.Package), false)) > 0
		lp.SolutionUnlocked = lp.Attempted && lp.HasSolution
		if rec != nil {
			for i := len(rec.LabAttempts) - 1; i >= 0; i-- {
				if rec.LabAttempts[i].Package == l.Package {
					a := rec.LabAttempts[i]
					lp.Last = &a
					break
				}
			}
			// A later full run is newer evidence than an older single-package attempt.
			if n := len(rec.LabRuns); n > 0 {
				run := rec.LabRuns[n-1]
				for _, pk := range run.Packages {
					if pk.Package == l.Package && (lp.Last == nil || run.At.After(lp.Last.At)) {
						lp.Last = &progress.LabAttempt{At: run.At, Package: pk.Package, Passed: pk.Passed, Race: run.Race, Failed: pk.Failed}
					}
				}
			}
		}
		resp.Packages = append(resp.Packages, lp)
	}
	if rec != nil && len(rec.LabRuns) > 0 {
		run := rec.LabRuns[len(rec.LabRuns)-1]
		resp.LastRun = &run
	}
	if m.Requires(course.GateLab) {
		for _, c := range mastery.Gate(st.c, m, st.p) {
			if c.Name == course.GateLab {
				c := c
				resp.Gate = &c
			}
		}
	}
	return resp
}

// goFiles lists the .go files in a repo-relative directory: implementation files
// first, then tests (only when withTests).
func goFiles(c *course.Course, dir string, withTests bool) []string {
	ents, err := os.ReadDir(c.Path(dir))
	if err != nil {
		return []string{}
	}
	var impl, tests []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") {
			continue
		}
		if strings.HasSuffix(n, "_test.go") {
			tests = append(tests, n)
		} else {
			impl = append(impl, n)
		}
	}
	sort.Strings(impl)
	sort.Strings(tests)
	if withTests {
		impl = append(impl, tests...)
	}
	if impl == nil {
		impl = []string{}
	}
	return impl
}

type runRequest struct {
	Verbose bool `json:"verbose"`
}

type runResponse struct {
	Output  string          `json:"output"`
	Race    bool            `json:"race"`
	Passed  bool            `json:"passed"`
	Run     progress.LabRun `json:"run"`
	Gates   []string        `json:"gates_passed"`
	Labs    labsResponse    `json:"labs"`
	Seconds float64         `json:"seconds"`
}

const maxOutput = 512 << 10

// capped keeps the first maxOutput bytes of test output.
type capped struct{ bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := maxOutput - c.Len(); room > 0 {
		if len(p) > room {
			c.Buffer.Write(p[:room])
			c.Buffer.WriteString("\n… output truncated …\n")
		} else {
			c.Buffer.Write(p)
		}
	}
	return len(p), nil
}

// handleLabRun runs one lab package's tests (the "Run tests" button). The attempt
// unlocks that package's solution but is never a gate run.
func (s *Server) handleLabRun(w http.ResponseWriter, r *http.Request) {
	s.runLab(w, r, r.PathValue("pkg"))
}

// handleLabRunAll runs every core package, exactly like `sd lab`, and records the run
// for the gate.
func (s *Server) handleLabRunAll(w http.ResponseWriter, r *http.Request) {
	s.runLab(w, r, "")
}

func (s *Server) runLab(w http.ResponseWriter, r *http.Request, pkg string) {
	var req runRequest
	if err := decode(r, &req); err != nil {
		reply(w, nil, err)
		return
	}
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
	if !m.Written() {
		reply(w, nil, errorf(http.StatusBadRequest, "%s is planned but not written yet", m.ID))
		return
	}
	opt := labrun.Options{Timeout: 5 * time.Minute, Verbose: req.Verbose}
	if pkg != "" {
		found := false
		for _, l := range m.Labs {
			found = found || l.Package == pkg
		}
		if !found {
			reply(w, nil, errorf(http.StatusNotFound, "%s has no lab package %q", m.ID, pkg))
			return
		}
		opt.Packages = []string{pkg}
	}

	s.labMu.Lock()
	defer s.labMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	opt.Race = s.raceAvailable(ctx)
	var out capped
	start := time.Now()
	run, err := labrun.Run(ctx, st.c.Root, m, opt, &out)
	if err != nil {
		reply(w, nil, errorf(http.StatusInternalServerError, "%v\n%s", err, out.String()))
		return
	}
	resp := runResponse{Output: out.String(), Race: opt.Race, Seconds: time.Since(start).Seconds(), Gates: []string{}}

	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.mutate(func(st *state) error {
		now := s.now()
		run.At = now
		if pkg != "" {
			lp := run.Packages[0]
			resp.Passed = lp.Passed
			mastery.RecordLabAttempt(st.p, m.ID, progress.LabAttempt{At: now, Package: pkg, Passed: lp.Passed, Race: run.Race, Failed: lp.Failed})
		} else {
			resp.Passed = run.Passed
			// Same rule as `sd lab`: the race detector is on whenever cgo allows it.
			resp.Gates = append(resp.Gates, mastery.RecordLab(st.c, st.p, m.ID, run)...)
		}
		resp.Run = run
		return nil
	})
	if err != nil {
		reply(w, nil, err)
		return
	}
	st, err = s.load()
	if err == nil {
		resp.Labs = s.labsView(ctx, st, m)
	}
	reply(w, resp, err)
}

type solutionFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	Test    bool   `json:"test"`
}

type solutionResponse struct {
	Module  string         `json:"module"`
	Package string         `json:"package"`
	Dir     string         `json:"dir"`
	Files   []solutionFile `json:"files"`
}

// handleSolution serves a reference solution, but only after the learner has run that
// lab at least once. Test files are listed but not repeated: they're identical to the lab's.
func (s *Server) handleSolution(w http.ResponseWriter, r *http.Request) {
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
	pkg := r.PathValue("pkg")
	found := false
	for _, l := range m.Labs {
		found = found || l.Package == pkg
	}
	if !found || m.Paths.Solution == "" {
		reply(w, nil, errorf(http.StatusNotFound, "%s has no solution for %q", m.ID, pkg))
		return
	}
	if !st.p.Peek(m.ID).Attempted(pkg) {
		reply(w, nil, errorf(http.StatusForbidden, "run the %s tests at least once before opening the reference solution", pkg))
		return
	}
	dir := path.Join(m.Paths.Solution, pkg)
	resp := solutionResponse{Module: m.ID, Package: pkg, Dir: dir, Files: []solutionFile{}}
	for _, name := range goFiles(st.c, dir, true) {
		f := solutionFile{Name: name, Test: strings.HasSuffix(name, "_test.go")}
		if !f.Test {
			b, err := os.ReadFile(st.c.Path(path.Join(dir, name)))
			if err != nil {
				reply(w, nil, err)
				return
			}
			f.Content = string(b)
		}
		resp.Files = append(resp.Files, f)
	}
	reply(w, resp, nil)
}
