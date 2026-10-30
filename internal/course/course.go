// Package course loads the course definition: course/course.json (tracks, policy,
// recommended order) and one modules/<ID>-<slug>/module.json per module, plus each
// module's quiz.json. Everything here is plain data with JSON tags so that the CLI and
// any future UI read exactly the same files.
package course

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SchemaVersion is the version of course.json and module.json this code understands.
const SchemaVersion = 1

// Course is the parsed course/course.json plus every module found under modules/.
type Course struct {
	SchemaVersion    int      `json:"schema_version"`
	Title            string   `json:"title"`
	Tracks           []Track  `json:"tracks"`
	Policy           Policy   `json:"policy"`
	RecommendedOrder []string `json:"recommended_order"`

	// Populated by Load, not read from course.json.
	Root    string             `json:"-"`
	Modules map[string]*Module `json:"-"`
}

// Track is one of the course tracks (A..E).
type Track struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Policy holds every tunable threshold of the mastery system.
type Policy struct {
	QuizPassThreshold      float64  `json:"quiz_pass_threshold"`
	QuizPassesRequired     int      `json:"quiz_passes_required"`
	QuizPassesDistinctDays bool     `json:"quiz_passes_distinct_days"`
	DefaultQuizDraw        int      `json:"default_quiz_draw"`
	RubricLevels           []string `json:"rubric_levels"`
	DesignMinLevel         string   `json:"design_min_level"`
	MockMinLevel           string   `json:"mock_min_level"`
	MockDistinctProblems   bool     `json:"mock_distinct_problems"`
	ReviewIntervalsDays    []int    `json:"review_intervals_days"`
	ReviewSessionSize      int      `json:"review_session_size"`
	ReviewRefreshThreshold float64  `json:"review_refresh_threshold"`
	ReviewRefreshWindow    int      `json:"review_refresh_window"`
	ReviewRefreshMinItems  int      `json:"review_refresh_min_items"`
	ReviewOverdueDays      int      `json:"review_overdue_days"`
	RecapGapDays           int      `json:"recap_gap_days"`
	EstimateTolerance      float64  `json:"estimate_tolerance"`
	EstimateMasteryWindow  int      `json:"estimate_mastery_window"`
	EstimateMasteryRate    float64  `json:"estimate_mastery_rate"`
}

// Hours is a CORE/STRETCH split of estimated effort.
type Hours struct {
	Core    float64 `json:"core"`
	Stretch float64 `json:"stretch"`
	Total   float64 `json:"total,omitempty"`
}

// Gate lists the components a module's mastery gate requires.
type Gate struct {
	Requires      []string `json:"requires"`
	MocksRequired int      `json:"mocks_required,omitempty"`
	MockFormats   []string `json:"mock_formats,omitempty"` // formats that count; empty = any
}

// Gate component names.
const (
	GateQuiz      = "quiz"
	GateLab       = "lab"
	GateDesign    = "design"
	GateTeachback = "teachback"
	GateMocks     = "mocks"
)

// Mock formats (Track D): a 45-minute HLD round, a 45-minute LLD round, a 90-minute
// machine-coding round, and a full loop of all three.
const (
	FormatHLD45 = "hld-45"
	FormatLLD45 = "lld-45"
	FormatMC90  = "mc-90"
	FormatLoop  = "loop"
)

// MockFormats lists every format a mock can be recorded in.
var MockFormats = []string{FormatHLD45, FormatLLD45, FormatMC90, FormatLoop}

// KnownMockFormat reports whether f is one of MockFormats.
func KnownMockFormat(f string) bool {
	for _, k := range MockFormats {
		if k == f {
			return true
		}
	}
	return false
}

// Paths are repo-root-relative locations of a module's files.
type Paths struct {
	Dir            string `json:"dir"`
	Readme         string `json:"readme,omitempty"`
	Quiz           string `json:"quiz,omitempty"`
	DesignExercise string `json:"design_exercise,omitempty"`
	Teachback      string `json:"teachback,omitempty"`
	Lab            string `json:"lab,omitempty"`
	Solution       string `json:"solution,omitempty"`
}

// Unit is one ~6h weekly unit of a module.
type Unit struct {
	N     int      `json:"n"`
	Title string   `json:"title"`
	File  string   `json:"file"`
	Hours Hours    `json:"hours"`
	Labs  []string `json:"labs,omitempty"`
}

// Lab is one Go lab package (labs/<id>/<package>, solutions/<id>/<package>).
type Lab struct {
	Package string `json:"package"`
	Title   string `json:"title"`
	Unit    int    `json:"unit"`
	Core    bool   `json:"core"`
}

// Outcome is a testable learning outcome.
type Outcome struct {
	Text string `json:"text"`
	Core bool   `json:"core"`
}

// Module is the parsed modules/<ID>-<slug>/module.json.
type Module struct {
	SchemaVersion int       `json:"schema_version"`
	ID            string    `json:"id"`
	Slug          string    `json:"slug"`
	Title         string    `json:"title"`
	Track         string    `json:"track"`
	Status        string    `json:"status"` // "written" or "planned"
	Core          bool      `json:"core"`
	Summary       string    `json:"summary"`
	Hours         Hours     `json:"hours"`
	Prerequisites []string  `json:"prerequisites"`
	Gate          Gate      `json:"gate"`
	Paths         Paths     `json:"paths"`
	Units         []Unit    `json:"units,omitempty"`
	Labs          []Lab     `json:"labs,omitempty"`
	Outcomes      []Outcome `json:"outcomes,omitempty"`

	// MetaPath is where this module.json was read from (repo-relative). Not serialized.
	MetaPath string `json:"-"`
}

// Module status values.
const (
	StatusWritten = "written"
	StatusPlanned = "planned"
)

// Written reports whether the module's content exists.
func (m *Module) Written() bool { return m.Status == StatusWritten }

// Requires reports whether the module's gate includes component c.
func (m *Module) Requires(c string) bool {
	for _, r := range m.Gate.Requires {
		if r == c {
			return true
		}
	}
	return false
}

// AcceptsMockFormat reports whether a mock in format f counts towards this module's gate.
// A gate without mock_formats accepts any known format.
func (m *Module) AcceptsMockFormat(f string) bool {
	if !KnownMockFormat(f) {
		return false
	}
	if len(m.Gate.MockFormats) == 0 {
		return true
	}
	for _, g := range m.Gate.MockFormats {
		if g == f {
			return true
		}
	}
	return false
}

// CoreLabs returns the lab packages that count towards the gate.
func (m *Module) CoreLabs() []Lab {
	var out []Lab
	for _, l := range m.Labs {
		if l.Core {
			out = append(out, l)
		}
	}
	return out
}

// TotalHours is core+stretch (or the explicit total when given).
func (m *Module) TotalHours() float64 {
	if m.Hours.Total > 0 {
		return m.Hours.Total
	}
	return m.Hours.Core + m.Hours.Stretch
}

// ErrNoRoot is returned when no course root can be found.
var ErrNoRoot = errors.New("could not find course root (a directory containing course/course.json); run inside the repo or set SD_ROOT")

// FindRoot returns $SD_ROOT if set, otherwise walks up from start looking for course/course.json.
func FindRoot(start string) (string, error) {
	if env := os.Getenv("SD_ROOT"); env != "" {
		return filepath.Abs(env)
	}
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "course", "course.json")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrNoRoot
		}
		dir = parent
	}
}

// Load reads course/course.json and every modules/*/module.json under root.
func Load(root string) (*Course, error) {
	c := &Course{}
	if err := readJSON(filepath.Join(root, "course", "course.json"), c); err != nil {
		return nil, err
	}
	c.Root = root
	c.Modules = map[string]*Module{}
	c.Policy.applyDefaults()

	metas, err := filepath.Glob(filepath.Join(root, "modules", "*", "module.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(metas)
	for _, p := range metas {
		m := &Module{}
		if err := readJSON(p, m); err != nil {
			return nil, err
		}
		rel, _ := filepath.Rel(root, p)
		m.MetaPath = filepath.ToSlash(rel)
		if m.ID == "" {
			return nil, fmt.Errorf("%s: missing id", m.MetaPath)
		}
		if prev, dup := c.Modules[m.ID]; dup {
			return nil, fmt.Errorf("duplicate module id %s in %s and %s", m.ID, prev.MetaPath, m.MetaPath)
		}
		c.Modules[m.ID] = m
	}
	return c, nil
}

func (p *Policy) applyDefaults() {
	if p.QuizPassThreshold == 0 {
		p.QuizPassThreshold = 0.85
	}
	if p.QuizPassesRequired == 0 {
		p.QuizPassesRequired = 2
	}
	if p.DefaultQuizDraw == 0 {
		p.DefaultQuizDraw = 15
	}
	if len(p.RubricLevels) == 0 {
		p.RubricLevels = []string{"below", "approaching", "meets", "exceeds"}
	}
	if p.DesignMinLevel == "" {
		p.DesignMinLevel = "meets"
	}
	if p.MockMinLevel == "" {
		p.MockMinLevel = "meets"
	}
	if len(p.ReviewIntervalsDays) == 0 {
		p.ReviewIntervalsDays = []int{1, 3, 7, 14, 30, 60, 120}
	}
	if p.ReviewSessionSize == 0 {
		p.ReviewSessionSize = 20
	}
	if p.ReviewRefreshThreshold == 0 {
		p.ReviewRefreshThreshold = 0.70
	}
	if p.ReviewRefreshWindow == 0 {
		p.ReviewRefreshWindow = 10
	}
	if p.ReviewRefreshMinItems == 0 {
		p.ReviewRefreshMinItems = 5
	}
	if p.ReviewOverdueDays == 0 {
		p.ReviewOverdueDays = 45
	}
	if p.RecapGapDays == 0 {
		p.RecapGapDays = 14
	}
	if p.EstimateTolerance == 0 {
		p.EstimateTolerance = 0.25
	}
	if p.EstimateMasteryWindow == 0 {
		p.EstimateMasteryWindow = 20
	}
	if p.EstimateMasteryRate == 0 {
		p.EstimateMasteryRate = 0.80
	}
}

// LevelRank returns the index of a rubric level (higher is better), or -1 if unknown.
func (p Policy) LevelRank(level string) int {
	for i, l := range p.RubricLevels {
		if strings.EqualFold(l, level) {
			return i
		}
	}
	return -1
}

// Module looks up a module by id, case-insensitively.
func (c *Course) Module(id string) (*Module, error) {
	if m, ok := c.Modules[strings.ToUpper(id)]; ok {
		return m, nil
	}
	return nil, fmt.Errorf("unknown module %q (see `sd status` for the list)", id)
}

// Ordered returns modules in recommended order; modules missing from the order follow, sorted by id.
func (c *Course) Ordered() []*Module {
	seen := map[string]bool{}
	var out []*Module
	for _, id := range c.RecommendedOrder {
		if m, ok := c.Modules[id]; ok && !seen[id] {
			out = append(out, m)
			seen[id] = true
		}
	}
	var rest []string
	for id := range c.Modules {
		if !seen[id] {
			rest = append(rest, id)
		}
	}
	sort.Strings(rest)
	for _, id := range rest {
		out = append(out, c.Modules[id])
	}
	return out
}

// Path resolves a repo-relative path against the course root.
func (c *Course) Path(rel string) string {
	return filepath.Join(c.Root, filepath.FromSlash(rel))
}

// LoadQuiz reads the quiz for a module. Returns an error if the module has no quiz.
func (c *Course) LoadQuiz(m *Module) (*Quiz, error) {
	if m.Paths.Quiz == "" {
		return nil, fmt.Errorf("module %s has no quiz", m.ID)
	}
	if !m.Written() {
		return nil, fmt.Errorf("module %s is planned but not written yet", m.ID)
	}
	q := &Quiz{}
	if err := readJSON(c.Path(m.Paths.Quiz), q); err != nil {
		return nil, err
	}
	return q, nil
}

// QuestionIndex loads every written module's quiz and indexes questions by id.
// Modules whose quiz fails to load are skipped (Validate reports them).
func (c *Course) QuestionIndex() map[string]*Question {
	idx := map[string]*Question{}
	for _, m := range c.Modules {
		if !m.Written() || m.Paths.Quiz == "" {
			continue
		}
		q, err := c.LoadQuiz(m)
		if err != nil {
			continue
		}
		for i := range q.Questions {
			qq := &q.Questions[i]
			qq.Module = m.ID
			idx[qq.ID] = qq
		}
	}
	return idx
}

// readJSON is lenient about unknown fields so that newer files don't break older
// tooling; Validate re-reads strictly to catch typos.
func readJSON(path string, v any) error { return decodeJSON(path, v, false) }

func decodeJSON(path string, v any, strict bool) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
