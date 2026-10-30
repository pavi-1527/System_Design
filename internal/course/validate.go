package course

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/chethu007/system-design-course/internal/quantity"
)

// Issue is one validation finding.
type Issue struct {
	Severity string `json:"severity"` // "error" or "warning"
	Path     string `json:"path"`
	Message  string `json:"message"`
}

func (i Issue) String() string { return fmt.Sprintf("%-7s %s: %s", i.Severity, i.Path, i.Message) }

type validator struct {
	c      *Course
	issues []Issue
}

func (v *validator) errf(path, format string, a ...any) {
	v.issues = append(v.issues, Issue{"error", path, fmt.Sprintf(format, a...)})
}

func (v *validator) warnf(path, format string, a ...any) {
	v.issues = append(v.issues, Issue{"warning", path, fmt.Sprintf(format, a...)})
}

func (v *validator) exists(rel string) bool {
	_, err := os.Stat(v.c.Path(rel))
	return err == nil
}

var validGate = map[string]bool{GateQuiz: true, GateLab: true, GateDesign: true, GateTeachback: true, GateMocks: true}

// Validate checks course.json, every module.json, quizzes, lesson markup and the
// lab/solution contract. It returns all issues found (errors and warnings).
func Validate(c *Course) []Issue {
	v := &validator{c: c}
	v.course()
	ids := make([]string, 0, len(c.Modules))
	for id := range c.Modules {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		v.module(c.Modules[id])
	}
	v.cycles()
	return v.issues
}

func (v *validator) course() {
	c := v.c
	const p = "course/course.json"
	strict := &Course{}
	if err := decodeJSON(c.Path(p), strict, true); err != nil {
		v.errf(p, "%v", err)
	}
	if c.SchemaVersion != SchemaVersion {
		v.errf(p, "schema_version %d, expected %d", c.SchemaVersion, SchemaVersion)
	}
	if len(c.Tracks) == 0 {
		v.errf(p, "no tracks defined")
	}
	seen := map[string]bool{}
	for _, id := range c.RecommendedOrder {
		if seen[id] {
			v.errf(p, "recommended_order lists %s twice", id)
		}
		seen[id] = true
		if _, ok := c.Modules[id]; !ok {
			v.errf(p, "recommended_order lists unknown module %s", id)
		}
	}
	for id := range c.Modules {
		if !seen[id] {
			v.warnf(p, "module %s is missing from recommended_order", id)
		}
	}
}

func (v *validator) trackOK(t string) bool {
	for _, tr := range v.c.Tracks {
		if tr.ID == t {
			return true
		}
	}
	return false
}

func (v *validator) module(m *Module) {
	p := m.MetaPath
	strict := &Module{}
	if err := decodeJSON(v.c.Path(p), strict, true); err != nil {
		v.errf(p, "%v", err)
	}
	if m.SchemaVersion != SchemaVersion {
		v.errf(p, "schema_version %d, expected %d", m.SchemaVersion, SchemaVersion)
	}
	wantDir := "modules/" + m.ID + "-" + m.Slug
	if filepath.ToSlash(filepath.Dir(p)) != wantDir {
		v.errf(p, "module.json should live in %s/ (id-slug)", wantDir)
	}
	if m.Paths.Dir != wantDir {
		v.errf(p, "paths.dir is %q, expected %q", m.Paths.Dir, wantDir)
	}
	if m.Title == "" {
		v.errf(p, "missing title")
	}
	if !v.trackOK(m.Track) {
		v.errf(p, "unknown track %q", m.Track)
	}
	if !strings.HasPrefix(m.ID, m.Track) {
		v.errf(p, "id %s does not start with its track %s", m.ID, m.Track)
	}
	if m.Status != StatusWritten && m.Status != StatusPlanned {
		v.errf(p, "status must be %q or %q", StatusWritten, StatusPlanned)
	}
	if m.Hours.Core+m.Hours.Stretch <= 0 {
		v.errf(p, "hours.core + hours.stretch must be > 0")
	}
	if m.Hours.Total != 0 && abs(m.Hours.Total-(m.Hours.Core+m.Hours.Stretch)) > 0.01 {
		v.errf(p, "hours.total %.1f != core %.1f + stretch %.1f", m.Hours.Total, m.Hours.Core, m.Hours.Stretch)
	}
	for _, pre := range m.Prerequisites {
		if pre == m.ID {
			v.errf(p, "module lists itself as a prerequisite")
		} else if _, ok := v.c.Modules[pre]; !ok {
			v.errf(p, "unknown prerequisite %s", pre)
		}
	}
	if len(m.Gate.Requires) == 0 {
		v.errf(p, "gate.requires is empty")
	}
	for _, g := range m.Gate.Requires {
		if !validGate[g] {
			v.errf(p, "unknown gate component %q", g)
		}
	}
	if m.Requires(GateMocks) && m.Gate.MocksRequired <= 0 {
		v.errf(p, "gate requires mocks but mocks_required is not set")
	}
	if len(m.Gate.MockFormats) > 0 && !m.Requires(GateMocks) {
		v.errf(p, "gate.mock_formats is set but the gate does not require mocks")
	}
	for _, f := range m.Gate.MockFormats {
		if !KnownMockFormat(f) {
			v.errf(p, "unknown mock format %q (use one of %s)", f, strings.Join(MockFormats, ", "))
		}
	}
	if !m.Written() {
		return
	}

	// Written modules: every referenced file must exist and agree.
	need := func(field, rel string, required bool) {
		switch {
		case rel == "" && required:
			v.errf(p, "paths.%s is required for this module's gate", field)
		case rel != "" && !v.exists(rel):
			v.errf(p, "paths.%s %q does not exist", field, rel)
		}
	}
	need("dir", m.Paths.Dir, true)
	need("readme", m.Paths.Readme, true)
	need("quiz", m.Paths.Quiz, m.Requires(GateQuiz))
	need("design_exercise", m.Paths.DesignExercise, m.Requires(GateDesign))
	need("teachback", m.Paths.Teachback, m.Requires(GateTeachback))
	need("lab", m.Paths.Lab, m.Requires(GateLab))
	need("solution", m.Paths.Solution, m.Requires(GateLab))

	if len(m.Units) == 0 {
		v.errf(p, "written module has no units")
	}
	var core, stretch float64
	labNames := map[string]bool{}
	for _, l := range m.Labs {
		labNames[l.Package] = true
	}
	for _, u := range m.Units {
		core += u.Hours.Core
		stretch += u.Hours.Stretch
		if u.File == "" || !v.exists(u.File) {
			v.errf(p, "unit %d file %q does not exist", u.N, u.File)
		}
		for _, l := range u.Labs {
			if !labNames[l] {
				v.errf(p, "unit %d references lab %q not listed in labs", u.N, l)
			}
		}
	}
	if abs(core-m.Hours.Core) > 0.01 || abs(stretch-m.Hours.Stretch) > 0.01 {
		v.errf(p, "unit hours (core %.1f, stretch %.1f) don't add up to module hours (core %.1f, stretch %.1f)", core, stretch, m.Hours.Core, m.Hours.Stretch)
	}
	if m.Requires(GateLab) && len(m.CoreLabs()) == 0 {
		v.errf(p, "gate requires lab but no core labs are listed")
	}
	for _, l := range m.Labs {
		v.labPair(p, m, l)
	}
	if m.Paths.Quiz != "" && v.exists(m.Paths.Quiz) {
		v.quiz(m)
	}
	v.markdown(m)
}

func (v *validator) labPair(p string, m *Module, l Lab) {
	labDir := m.Paths.Lab + "/" + l.Package
	solDir := m.Paths.Solution + "/" + l.Package
	if !v.exists(labDir) {
		v.errf(p, "lab package %s does not exist", labDir)
		return
	}
	if !v.exists(solDir) {
		v.errf(p, "solution package %s does not exist", solDir)
		return
	}
	labTests, _ := filepath.Glob(v.c.Path(labDir + "/*_test.go"))
	solTests, _ := filepath.Glob(v.c.Path(solDir + "/*_test.go"))
	if len(labTests) == 0 {
		v.errf(labDir, "no *_test.go files")
	}
	names := func(fs []string) map[string]bool {
		out := map[string]bool{}
		for _, f := range fs {
			out[filepath.Base(f)] = true
		}
		return out
	}
	ln, sn := names(labTests), names(solTests)
	for n := range ln {
		if !sn[n] {
			v.errf(solDir, "missing test file %s (present in lab)", n)
			continue
		}
		a, _ := os.ReadFile(v.c.Path(labDir + "/" + n))
		b, _ := os.ReadFile(v.c.Path(solDir + "/" + n))
		if !bytes.Equal(a, b) {
			v.errf(labDir+"/"+n, "differs from %s/%s; lab and solution tests must be byte-identical", solDir, n)
		}
	}
	for n := range sn {
		if !ln[n] {
			v.errf(labDir, "missing test file %s (present in solution)", n)
		}
	}
}

var badChoice = regexp.MustCompile(`(?i)\b(all|none|both) of the above\b|\bboth [a-d] and [a-d]\b|\boption [a-d]\b`)

func (v *validator) quiz(m *Module) {
	p := m.Paths.Quiz
	q := &Quiz{}
	if err := decodeJSON(v.c.Path(p), q, true); err != nil {
		v.errf(p, "%v", err)
		return
	}
	if q.Module != m.ID {
		v.errf(p, "module is %q, expected %q", q.Module, m.ID)
	}
	draw := q.Draw
	if draw == 0 {
		draw = v.c.Policy.DefaultQuizDraw
	}
	if len(q.Questions) < 2*draw {
		v.errf(p, "pool has %d questions; need at least 2 × draw (%d)", len(q.Questions), 2*draw)
	}
	seen := map[string]bool{}
	idRe := regexp.MustCompile("^" + regexp.QuoteMeta(m.ID) + `-\d{3}$`)
	for i, qq := range q.Questions {
		where := fmt.Sprintf("%s question #%d (%s)", p, i+1, qq.ID)
		if !idRe.MatchString(qq.ID) {
			v.errf(where, "id must look like %s-NNN", m.ID)
		}
		if seen[qq.ID] {
			v.errf(where, "duplicate id")
		}
		seen[qq.ID] = true
		if strings.TrimSpace(qq.Prompt) == "" {
			v.errf(where, "empty prompt")
		}
		if strings.TrimSpace(qq.Explanation) == "" {
			v.errf(where, "empty explanation")
		}
		if len(qq.Tags) == 0 {
			v.errf(where, "no tags")
		}
		if qq.Ref == "" {
			v.errf(where, "no ref")
		} else if file := strings.SplitN(qq.Ref, "#", 2)[0]; file != "" && !v.exists(m.Paths.Dir+"/"+file) {
			v.errf(where, "ref file %q does not exist in %s", file, m.Paths.Dir)
		}
		switch qq.Type {
		case QSingle, QMulti:
			if len(qq.Choices) < 2 {
				v.errf(where, "needs at least 2 choices")
			}
			if len(qq.Answer) == 0 {
				v.errf(where, "no answer")
			}
			if qq.Type == QSingle && len(qq.Answer) != 1 {
				v.errf(where, "single-choice question must have exactly one answer")
			}
			dup := map[int]bool{}
			for _, a := range qq.Answer {
				if a < 0 || a >= len(qq.Choices) {
					v.errf(where, "answer index %d out of range (0-%d)", a, len(qq.Choices)-1)
				}
				if dup[a] {
					v.errf(where, "answer index %d repeated", a)
				}
				dup[a] = true
			}
			for _, ch := range qq.Choices {
				if badChoice.MatchString(ch) {
					v.errf(where, "choice %q refers to other choices; choices are shuffled", ch)
				}
			}
		case QNumeric:
			if qq.Value == 0 {
				v.errf(where, "numeric question needs a non-zero value")
			}
			if qq.Tolerance <= 0 || qq.Tolerance > 1 {
				v.errf(where, "tolerance must be in (0, 1], got %g", qq.Tolerance)
			}
			if len(qq.Choices) > 0 || len(qq.Answer) > 0 {
				v.errf(where, "numeric question must not have choices/answer")
			}
			u := quantity.ParseUnit(qq.Unit)
			if got, _, err := u.Parse(quantity.Trim(qq.Value)); err != nil || !quantity.Within(got, qq.Value*u.Scale, 0.01) {
				v.warnf(where, "unit %q does not round-trip cleanly", qq.Unit)
			}
		default:
			v.errf(where, "type must be single, multi or numeric")
		}
	}
}

var (
	predictOpenRe  = regexp.MustCompile(`<!--\s*predict\s+id="([^"]+)"\s*-->`)
	predictCloseRe = regexp.MustCompile(`<!--\s*/predict\s*-->`)
	detailsRe      = regexp.MustCompile(`<details\b([^>]*)>`)
	attrRe         = regexp.MustCompile(`([a-z-]+)="([^"]*)"`)
)

// Markup is what a lesson file contains, in a form a UI can use too.
type Markup struct {
	Predicts []string          // predict ids in order
	Reveals  map[string]int    // data-for → count
	Kinds    map[string]int    // data-kind → count
	Bad      []string          // problems found while scanning
	Order    map[string][2]int // predict id → [predict offset, reveal offset]
}

// ScanMarkup finds predict blocks and <details> blocks in a lesson.
func ScanMarkup(src string) Markup {
	mk := Markup{Reveals: map[string]int{}, Kinds: map[string]int{}, Order: map[string][2]int{}}
	opens := predictOpenRe.FindAllStringSubmatchIndex(src, -1)
	closes := predictCloseRe.FindAllStringIndex(src, -1)
	if len(opens) != len(closes) {
		mk.Bad = append(mk.Bad, fmt.Sprintf("%d predict openings but %d <!-- /predict --> closings", len(opens), len(closes)))
	}
	for i, o := range opens {
		id := src[o[2]:o[3]]
		mk.Predicts = append(mk.Predicts, id)
		mk.Order[id] = [2]int{o[0], -1}
		if i < len(closes) && closes[i][0] < o[1] {
			mk.Bad = append(mk.Bad, fmt.Sprintf("predict %s: closing marker before opening", id))
		}
		if i+1 < len(opens) && i < len(closes) && closes[i][0] > opens[i+1][0] {
			mk.Bad = append(mk.Bad, fmt.Sprintf("predict %s is not closed before the next predict block", id))
		}
	}
	for _, d := range detailsRe.FindAllStringSubmatchIndex(src, -1) {
		attrs := map[string]string{}
		for _, a := range attrRe.FindAllStringSubmatch(src[d[2]:d[3]], -1) {
			attrs[a[1]] = a[2]
		}
		kind := attrs["data-kind"]
		switch kind {
		case "reveal":
			f := attrs["data-for"]
			if f == "" {
				mk.Bad = append(mk.Bad, "reveal <details> without data-for")
				continue
			}
			mk.Reveals[f]++
			if o, ok := mk.Order[f]; ok && o[1] < 0 {
				mk.Order[f] = [2]int{o[0], d[0]}
			}
		case "answer", "hint":
		case "":
			line := strings.Count(src[:d[0]], "\n") + 1
			mk.Bad = append(mk.Bad, fmt.Sprintf("line %d: <details> without data-kind", line))
			continue
		default:
			mk.Bad = append(mk.Bad, fmt.Sprintf("unknown data-kind %q", kind))
			continue
		}
		mk.Kinds[kind]++
	}
	return mk
}

func (v *validator) markdown(m *Module) {
	files, _ := filepath.Glob(v.c.Path(m.Paths.Dir + "/*.md"))
	seen := map[string]string{}
	for _, f := range files {
		rel, _ := filepath.Rel(v.c.Root, f)
		rel = filepath.ToSlash(rel)
		b, err := os.ReadFile(f)
		if err != nil {
			v.errf(rel, "%v", err)
			continue
		}
		mk := ScanMarkup(string(b))
		for _, bad := range mk.Bad {
			v.errf(rel, "%s", bad)
		}
		for _, id := range mk.Predicts {
			if prev, dup := seen[id]; dup {
				v.errf(rel, "predict id %s already used in %s", id, prev)
			}
			seen[id] = rel
			if !strings.HasPrefix(id, m.ID+"-") {
				v.errf(rel, "predict id %s should start with %s-", id, m.ID)
			}
			switch n := mk.Reveals[id]; {
			case n == 0:
				v.errf(rel, "predict %s has no <details data-kind=\"reveal\" data-for=%q>", id, id)
			case n > 1:
				v.errf(rel, "predict %s has %d reveals", id, n)
			default:
				if o := mk.Order[id]; o[1] < o[0] {
					v.errf(rel, "reveal for %s appears before its predict block", id)
				}
			}
		}
		for f := range mk.Reveals {
			if _, ok := mk.Order[f]; !ok {
				v.errf(rel, "reveal data-for=%q has no matching predict block in this file", f)
			}
		}
	}
	for _, u := range m.Units {
		b, err := os.ReadFile(v.c.Path(u.File))
		if err == nil && len(ScanMarkup(string(b)).Predicts) == 0 {
			v.errf(u.File, "unit has no predict blocks")
		}
	}
}

func (v *validator) cycles() {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var visit func(id string, path []string)
	visit = func(id string, path []string) {
		color[id] = grey
		for _, pre := range v.c.Modules[id].Prerequisites {
			if _, ok := v.c.Modules[pre]; !ok {
				continue
			}
			switch color[pre] {
			case grey:
				v.errf(v.c.Modules[id].MetaPath, "prerequisite cycle: %s", strings.Join(append(path, id, pre), " → "))
			case white:
				visit(pre, append(path, id))
			}
		}
		color[id] = black
	}
	ids := make([]string, 0, len(v.c.Modules))
	for id := range v.c.Modules {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if color[id] == white {
			visit(id, nil)
		}
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
